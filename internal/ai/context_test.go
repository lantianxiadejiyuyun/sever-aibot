package ai

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPrepareContextExtractsWithoutChangingOriginalHistory(t *testing.T) {
	history := []Message{{Role: "user", Content: strings.Repeat("你", 2000)}, {Role: "assistant", Content: strings.Repeat("old answer", 600)}, {Role: "user", Content: "recent question"}, {Role: "assistant", Content: "recent answer"}}
	messages, b, err := PrepareContext("system", history, "current question", 3000, 200)
	if err != nil || b.SummaryMethod == "" || b.SummaryBytes == 0 || b.DroppedMessages != 2 || b.EstimatedInput+b.OutputReserve > 3000 {
		t.Fatalf("budget %+v err %v", b, err)
	}
	last := messages[len(messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "untrusted_history_excerpts") || !strings.HasSuffix(last.Content, "current question") || !utf8.ValidString(last.Content) {
		t.Fatalf("bad context %+v", last)
	}
	if len(history[0].Content) != 6000 || messages[0].Content != "system" {
		t.Fatal("original history or system modified")
	}
}
func TestPrepareContextSmallWindowFallsBackToExplicitTrim(t *testing.T) {
	_, b, err := PrepareContext("system", []Message{{Role: "user", Content: strings.Repeat("x", 400)}, {Role: "assistant", Content: "answer"}}, "current", 180, 20)
	if err != nil || b.SummaryMethod != "" || b.DroppedMessages != 2 || b.EstimatedInput+b.OutputReserve > 180 {
		t.Fatalf("%+v %v", b, err)
	}
}
func TestRegisteredToolSchemasValid(t *testing.T) {
	for _, schema := range ToolDefinitions() {
		if !json.Valid(schema) {
			t.Fatalf("invalid tool schema %s", schema)
		}
	}
}
