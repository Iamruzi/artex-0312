package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestRecoveryToolEndToEndAndLegacyCompatibility(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { _ = d.Close() })
	task, err := d.CreateTask("recovery tool", "fixture only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	tools := NewToolSet(store, "planner")
	tools.SetTaskID(task.ID)
	initial, err := tools.addOneIntent(intentItem{Summary: "Inspect a local fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeState(initial, "exhausted"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendActivity(db.Activity{NodeID: &initial, Kind: "tool_result", Tool: "Bash", IsError: true, Detail: "ParserError secret-must-not-be-in-overview"}); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(map[string]any{"intents": []any{map[string]any{"summary": "Read the saved fixture using a corrected parser", "recovery": map[string]any{"previous_intent_id": initial, "reason": "shell_syntax", "change": "Use the host PowerShell file reader for the saved fixture", "expected_signal": "Decode one valid JSON object without a parser error"}}}})
	result, err := tools.addIntent().Call(context.Background(), request, nil)
	if err != nil || result.IsError {
		t.Fatalf("result=%s err=%v", result.Flatten(), err)
	}
	var out struct {
		IDs    []int64           `json:"ids"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal([]byte(result.Flatten()), &out); err != nil || len(out.IDs) != 1 || out.IDs[0] == 0 {
		t.Fatalf("%s err=%v", result.Flatten(), err)
	}
	overview := tools.graphOverviewData()
	raw, _ := json.Marshal(overview)
	if strings.Contains(string(raw), "secret-must-not-be-in-overview") {
		t.Fatal("raw error leaked into context")
	}
	if !strings.Contains(string(raw), "recovery_memory") || !strings.Contains(string(raw), "expected_signal") {
		t.Fatal("recovery context missing")
	}
	duplicate, _ := tools.addIntent().Call(context.Background(), request, nil)
	if !strings.Contains(duplicate.Flatten(), "已有相同") {
		t.Fatal(duplicate.Flatten())
	}
	invalid, _ := tools.addIntent().Call(context.Background(), json.RawMessage(`{"summary":"valid summary","recovery":false}`), nil)
	if !invalid.IsError {
		t.Fatal("malformed recovery silently accepted")
	}
	legacy, _ := tools.addIntent().Call(context.Background(), json.RawMessage(`{"summary":"A new independent fixture direction"}`), nil)
	if legacy.IsError {
		t.Fatal(legacy.Flatten())
	}
}

func TestShellEnvironmentMatchesExecutor(t *testing.T) {
	windows := shellEnvironment("windows")
	for _, want := range []string{"powershell -NoProfile", "curl.exe", "$LASTEXITCODE", "独立进程", "远端"} {
		if !strings.Contains(windows, want) {
			t.Fatalf("missing %q", want)
		}
	}
	unix := shellEnvironment("linux")
	if !strings.Contains(unix, "bash -c") || strings.Contains(unix, "当前宿主为 Windows") {
		t.Fatal(unix)
	}
}

func TestRecoveryMemoryBoundedClassifiedAndRedacted(t *testing.T) {
	first, second := int64(1), int64(2)
	items := []db.Activity{
		{ID: 9, NodeID: &first, Tool: "Bash", IsError: true, Detail: "ParserError API_KEY=do-not-copy"},
		{ID: 8, NodeID: &first, Tool: "Bash", IsError: true, Detail: "ParserError"},
		{ID: 7, NodeID: &second, Tool: "Bash", IsError: true, Summary: "Blocked by hook: ParserError"},
		{ID: 6, NodeID: &first, Tool: "WebFetch", IsError: true, Summary: "ParserError"},
		{ID: 5, NodeID: &first, Tool: "Bash", IsError: false, Summary: "ParserError"},
	}
	got := recoveryMemory(items)
	if len(got) != 3 || got[0]["count"] != 2 || got[0]["activity_id"] != int64(9) || got[1]["category"] != "policy_denied" || got[2]["category"] != "tool_error" {
		t.Fatalf("%+v", got)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "do-not-copy") || strings.Contains(string(raw), "ParserError") {
		t.Fatal("raw output leaked")
	}
	for i := int64(3); i < 40; i++ {
		id := i
		items = append(items, db.Activity{NodeID: &id, Tool: "Bash", IsError: true})
	}
	if len(recoveryMemory(items)) != 20 {
		t.Fatal("recovery memory must be bounded")
	}
}

func TestExecutionErrorCorrectedEarlyWithoutTreatingTargetAsBlocked(t *testing.T) {
	d := NewStuckDetector(DefaultStuckConfig())
	ev := d.Observe("Bash", "ParserError: && is not a valid statement separator", true)
	if ev == nil || ev.Kind != "shell_syntax" {
		t.Fatalf("%+v", ev)
	}
	msg, ok := d.Drain()
	if !ok || !strings.Contains(msg, "执行环境纠偏") || strings.Contains(msg, "信息梯度为零") {
		t.Fatal(msg)
	}
	if d.Observe("Bash", "ParserError: && is not a valid statement separator", true) != nil {
		t.Fatal("early correction repeated")
	}
	if d.Observe("WebFetch", "ParserError", true) != nil {
		t.Fatal("remote output classified as local parser failure")
	}
}

func TestRecoveryPromptAndMetadataSurviveCompaction(t *testing.T) {
	n := &db.Node{ID: 3, State: "open", Payload: json.RawMessage(`{"summary":"continue","recovery":{"previous_intent_id":1,"change":"use saved request","expected_signal":"valid response"}}`)}
	got := compactIntents([]*db.Node{n}, nil, nil)
	if got[0]["recovery"] == nil {
		t.Fatal("recovery metadata dropped")
	}
	for _, prompt := range []string{plannerSystem("goal", "data", "dir"), mainAgentSystem("goal", "data", "work"), workerSystem("", "", "data", "work", false)} {
		if !strings.Contains(prompt, "失败恢复与续派") || !strings.Contains(prompt, "本地命令执行环境") {
			t.Fatal("fixed guidance missing")
		}
	}
}
