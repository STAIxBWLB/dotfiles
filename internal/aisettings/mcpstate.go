package aisettings

// What this file holds used to live in claudemem_retire.go, the claude-mem
// retirement that dot no longer carries. Everything here is what the MCP config
// writers still need: the tri-state read of a server entry, and the JSON encoders
// that keep `<`, `>` and `&` as they are written. They are here rather than in
// vmem.go or amem.go because both use them, and the same server lookup answers
// for either name.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// presence is the tri-state result of looking for an MCP server in a config.
type presence int

const (
	absent presence = iota
	present
	unknown
)

// readFileState reads a file once: a missing file is absent evidence, any other error is unknown.
func readFileState(path string) ([]byte, presence, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		return raw, present, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, absent, nil
	}
	return nil, unknown, err
}

// mcpServerState reports whether a config registers the named MCP server. A file
// that is missing, or carries no mcpServers object, is absent; one that cannot be
// read or parsed is unknown.
func mcpServerState(path, name string) (presence, error) {
	raw, state, err := readFileState(path)
	if state != present {
		return state, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return absent, nil
	}
	var doc struct {
		Servers json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return unknown, fmt.Errorf("not valid JSON (%v)", err)
	}
	if len(doc.Servers) == 0 || string(bytes.TrimSpace(doc.Servers)) == "null" {
		return absent, nil
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(doc.Servers, &servers); err != nil {
		return unknown, fmt.Errorf("mcpServers is not an object (%v)", err)
	}
	if _, ok := servers[name]; ok {
		return present, nil
	}
	return absent, nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func marshalIndentNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
