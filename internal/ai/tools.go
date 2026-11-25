package ai

import (
	"encoding/json"
	"sort"
	"sync"
)

var toolRegistry = struct {
	sync.RWMutex
	definitions map[string]json.RawMessage
}{definitions: map[string]json.RawMessage{"propose_forward_reapply": json.RawMessage(ProposalTool)}}

// RegisterTool is for compile-time application extensions, never HTTP input.
func RegisterTool(name string, schema json.RawMessage) {
	var value struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(schema, &value) != nil || value.Type != "function" || value.Function.Name != name {
		panic("invalid AI tool schema")
	}
	toolRegistry.Lock()
	defer toolRegistry.Unlock()
	if _, exists := toolRegistry.definitions[name]; exists {
		panic("duplicate AI tool")
	}
	toolRegistry.definitions[name] = append(json.RawMessage(nil), schema...)
}
func ToolDefinitions() []json.RawMessage {
	toolRegistry.RLock()
	defer toolRegistry.RUnlock()
	keys := make([]string, 0, len(toolRegistry.definitions))
	for key := range toolRegistry.definitions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]json.RawMessage, 0, len(keys))
	for _, key := range keys {
		out = append(out, append(json.RawMessage(nil), toolRegistry.definitions[key]...))
	}
	return out
}
func ToolBudget() int {
	n := 64
	for _, schema := range ToolDefinitions() {
		n += len(schema) + 32
	}
	return n
}
