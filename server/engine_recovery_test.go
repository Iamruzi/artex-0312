package server

import (
	"context"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
)

func TestExecutionRecoveryHookPausesNextCallAndPreservesPlannerPriority(t *testing.T) {
	steer := ""
	var observed *agent.StuckEvent
	h := steerHooks{
		det:     agent.NewStuckDetector(agent.DefaultStuckConfig()),
		drain:   func() (string, bool) { m := steer; steer = ""; return m, m != "" },
		onStuck: func(ev *agent.StuckEvent) { observed = ev },
	}
	ctx := context.Background()
	h.PostToolUse(ctx, "Bash", []byte(`{}`), []byte("ParserError: invalid statement separator"), true)
	if observed == nil || observed.Kind != "shell_syntax" {
		t.Fatalf("event=%+v", observed)
	}
	steer = "Respect the existing scope"
	deny, msg, _ := h.PreToolUse(ctx, "Bash", []byte(`{}`))
	if !deny || !strings.Contains(msg, "Respect the existing scope") {
		t.Fatal(msg)
	}
	deny, msg, _ = h.PreToolUse(ctx, "Bash", []byte(`{}`))
	if !deny || !strings.Contains(msg, "执行环境纠偏") {
		t.Fatal(msg)
	}
	deny, _, _ = h.PreToolUse(ctx, "Bash", []byte(`{}`))
	if deny {
		t.Fatal("environment hint indefinitely blocks tools")
	}
}
