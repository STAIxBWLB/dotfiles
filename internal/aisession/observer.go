package aisession

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

var nativeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// observer forwards native output, keeping only one bounded event in memory.
// It intentionally never saves native output in dotfiles receipts.
type observer struct {
	agent             string
	output            io.Writer
	pending           []byte
	session           string
	completed, denied bool
	err               error
}

func newObserver(agent string, out io.Writer) *observer { return &observer{agent: agent, output: out} }
func (o *observer) Write(p []byte) (int, error) {
	if o.output != nil {
		if _, err := o.output.Write(p); err != nil {
			return 0, err
		}
	}
	if o.err != nil {
		return len(p), nil
	}
	o.pending = append(o.pending, p...)
	if len(o.pending) > 4<<20 {
		o.err = errors.New("native event exceeds 4 MiB; no retry")
		o.pending = nil
		return len(p), nil
	}
	for {
		i := bytes.IndexByte(o.pending, '\n')
		if i < 0 {
			break
		}
		o.event(o.pending[:i])
		o.pending = o.pending[i+1:]
	}
	return len(p), nil
}
func (o *observer) finish() error {
	if len(bytes.TrimSpace(o.pending)) > 0 {
		o.event(o.pending)
	}
	o.pending = nil
	return o.err
}
func (o *observer) event(line []byte) {
	if o.err != nil || len(bytes.TrimSpace(line)) == 0 {
		return
	}
	var event struct {
		Type              string            `json:"type"`
		Subtype           string            `json:"subtype"`
		SessionID         string            `json:"session_id"`
		ThreadID          string            `json:"thread_id"`
		IsError           bool              `json:"is_error"`
		PermissionDenials []json.RawMessage `json:"permission_denials"`
		Item              *struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Server string `json:"server"`
			Tool   string `json:"tool"`
			Result *struct {
				Content []json.RawMessage `json:"content"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"item"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		o.err = errors.New("invalid native JSON event; effects may be ambiguous")
		return
	}
	if o.agent == "claude" && len(event.PermissionDenials) > 0 {
		o.denied = true
	}

	// Codex 0.157.1 exec_events.rs has no approval.denied event. Its
	// item.completed statuses preserve command denial, but merge patch denial
	// with patch failure; MCP failures likewise cannot prove side-effect outcome.
	if o.agent == "codex" && event.Type == "item.completed" && event.Item != nil {
		switch event.Item.Type {
		case "command_execution":
			if event.Item.Status == "declined" {
				o.denied = true
			}
			// A failed test command is an ordinary completed process, not a denial.
		case "file_change":
			if event.Item.Status == "failed" {
				o.err = errors.New("native file change failed or was denied; reconciliation required before routing")
				return
			}
		case "mcp_tool_call":
			// A returned application error from a known lookup (for example, a
			// missing note) is not a native approval/transport error. Writes remain
			// ambiguous even if the server returned an application error envelope.
			returnedLookupError := event.Item.Error == nil && event.Item.Result != nil && event.Item.Result.Content != nil && readOnlyKnowledgeTool(event.Item.Server, event.Item.Tool)
			if event.Item.Error != nil || (event.Item.Status == "failed" && !returnedLookupError) {
				o.err = errors.New("native MCP call failed; approval or external effect may be ambiguous; no automatic routing")
				return
			}
		}
	}
	if event.IsError || event.Type == "turn.failed" || event.Type == "error" {
		o.err = errors.New("native error receipt; no retry")
		return
	}
	id := event.SessionID
	if o.agent == "codex" {
		id = event.ThreadID
	}
	if id != "" {
		if !nativeID.MatchString(id) {
			o.err = errors.New("invalid native session identifier")
			return
		}
		if o.session != "" && o.session != id {
			o.err = errors.New("conflicting native session identifiers")
			return
		}
		o.session = id
	}
	if o.agent == "claude" && event.Type == "result" {
		if event.Subtype != "success" {
			o.err = errors.New("native turn did not finish successfully")
			return
		}
		o.completed = true
	}
	if o.agent == "codex" && event.Type == "turn.completed" {
		o.completed = true
	}
}

// This intentionally excludes record/edit tools and unknown server aliases.
// Tool-result text never establishes permission authority.
func readOnlyKnowledgeTool(server, tool string) bool {
	switch server {
	case "vmem":
		// vmem's read tools. Every write, the two-step previews included, stays ambiguous on failure.
		switch tool {
		case "vmem_status", "vmem_search", "vmem_read", "vmem_read_many", "vmem_list", "vmem_links", "vmem_mocs", "vmem_check", "vmem_log_tail", "vmem_graph_neighbors":
			return true
		}
	case "amem":
		// amem's read tools. amem_remember and amem_work_state_write write, so a failed call
		// of either stays ambiguous and is never routed automatically.
		switch tool {
		case "amem_search", "amem_show", "amem_timeline", "amem_graph", "amem_work_state_read", "amem_vault_candidates", "amem_capacity":
			return true
		}
	}
	return false
}
