package aisession

import (
	"testing"
)

func TestCodexMissingNoteResultAllowsCompletedWrite(t *testing.T) {
	events := []string{
		`{"type":"thread.started","thread_id":"native-canary"}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"mcp_tool_call","server":"vmem","tool":"vmem_read","arguments":{"path":"new-note.md"},"result":{"content":[{"type":"text","text":"File not found: new-note.md"}],"structured_content":null},"error":null,"status":"failed"}}`,
		`{"type":"item.completed","item":{"id":"item_2","type":"mcp_tool_call","server":"vmem","tool":"vmem_create","arguments":{"path":"new-note.md"},"result":{"content":[{"type":"text","text":"Created note"}],"structured_content":null},"error":null,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"POLICY_RECORD_OK"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`,
	}
	observer := newObserver("codex", nil)
	for _, event := range events {
		_, _ = observer.Write([]byte(event + "\n"))
	}
	if err := observer.finish(); err != nil {
		t.Fatal(err)
	}
	if !observer.completed || observer.denied || observer.session != "native-canary" {
		t.Fatalf("unexpected observer: %+v", observer)
	}
}

func TestCodexMCPApplicationErrorsRemainNarrow(t *testing.T) {
	cases := []struct {
		name, server, tool, result, nativeError string
		blocked                                 bool
	}{
		{"missing vault note", "vmem", "vmem_read", `{"content":[{"type":"text","text":"File not found"}]}`, `null`, false},
		{"memory search application result", "amem", "amem_search", `{"content":[]}`, `null`, false},
		{"memory show of a missing id", "amem", "amem_show", `{"content":[]}`, `null`, false},
		{"work-list read", "amem", "amem_work_state_read", `{"content":[]}`, `null`, false},
		{"retired claude-mem lookup", "mcp-search", "search", `{"content":[]}`, `null`, true},
		{"retired plugin memory lookup", "plugin_claude-mem_mcp-search", "get_observations", `{"content":[]}`, `null`, true},
		{"native read denial", "vmem", "vmem_read", `null`, `{"message":"permission denied"}`, true},
		{"native error wins over result", "vmem", "vmem_read", `{"content":[]}`, `{"message":"permission denied"}`, true},
		{"missing result", "vmem", "vmem_read", `null`, `null`, true},
		{"malformed result envelope", "vmem", "vmem_read", `{}`, `null`, true},
		{"failed note write", "vmem", "vmem_create", `{"content":[{"type":"text","text":"File not found"}]}`, `null`, true},
		{"failed patch", "vmem", "vmem_patch", `{"content":[]}`, `null`, true},
		{"failed memory proposal", "amem", "amem_remember", `{"content":[]}`, `null`, true},
		{"failed work-list write", "amem", "amem_work_state_write", `{"content":[]}`, `null`, true},
		{"claude-mem tool name on amem", "amem", "observation_add", `{"content":[]}`, `null`, true},
		{"retired obsidian server", "obsidian", "read_note", `{"content":[]}`, `null`, true},
		{"unknown tool", "vmem", "vmem_delete", `{"content":[]}`, `null`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := `{"type":"item.completed","item":{"id":"item_1","type":"mcp_tool_call","server":"` + tc.server + `","tool":"` + tc.tool + `","result":` + tc.result + `,"error":` + tc.nativeError + `,"status":"failed"}}`
			observer := newObserver("codex", nil)
			_, _ = observer.Write([]byte(event + "\n" + `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`))
			if err := observer.finish(); (err != nil) != tc.blocked {
				t.Fatalf("blocked=%t error=%v", tc.blocked, err)
			}
		})
	}
}
