package db

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func recoveryStore(t *testing.T, d *DB) *ExplorationStore {
	t.Helper()
	id, err := d.CreateExploration("recovery test", "validate recovery")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, id) })
	return d.Exploration(id)
}

func TestRecoveryAdmissionIsolationLineageAndDuplicates(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	s := recoveryStore(t, d)
	other := recoveryStore(t, d)
	root, err := s.AddIntent(map[string]any{"summary": "initial"}, 5, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeState(root, "exhausted"); err != nil {
		t.Fatal(err)
	}
	plan := RecoveryPlan{PreviousIntentID: root, Reason: "budget", Change: "Read saved output before a new request", ExpectedSignal: "Observe whether the saved response is complete"}
	add := func(p RecoveryPlan) (int64, error) {
		return s.AddRecoveryIntent(map[string]any{"summary": "continue"}, 5, nil, p)
	}
	first, err := add(plan)
	if err != nil {
		t.Fatal(err)
	}
	whitespace := plan
	whitespace.Change = " READ   SAVED OUTPUT BEFORE A NEW REQUEST "
	if _, err := add(whitespace); err == nil {
		t.Fatal("case/space duplicate admitted")
	}
	if _, err := add(plan); err == nil {
		t.Fatal("duplicate admitted")
	}
	if err := s.SetNodeState(first, "exhausted"); err != nil {
		t.Fatal(err)
	}
	plan.PreviousIntentID = first
	if _, err := add(plan); err == nil {
		t.Fatal("same change admitted through descendant")
	}
	plan.Change = "Use a different local parser on the saved output"
	child, err := add(plan)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.GetNode(child)
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Recovery RecoveryPlan `json:"recovery"`
	}
	if err := json.Unmarshal(n.Payload, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Recovery.RootIntentID != root {
		t.Fatalf("root=%d", stored.Recovery.RootIntentID)
	}
	foreign, _ := other.AddIntent(map[string]any{"summary": "foreign"}, 5, nil, "")
	_ = other.SetNodeState(foreign, "exhausted")
	plan.PreviousIntentID = foreign
	if _, err := add(plan); err == nil {
		t.Fatal("foreign source admitted")
	}
	plan.PreviousIntentID = child
	for _, state := range []string{"open", "running", "stopped", "deleted"} {
		_ = s.SetNodeState(child, state)
		if _, err := add(plan); err == nil {
			t.Fatalf("source %s admitted", state)
		}
	}
	plan.PreviousIntentID = root
	plan.Change = "Use newly provided fixture evidence to refine the request"
	plan.Reason = "new_evidence"
	if _, err := add(plan); err == nil {
		t.Fatal("new evidence without references admitted")
	}
	fact, _ := s.AddNode(KindFact, map[string]any{"summary": "new observation"}, 0, "confirmed", "test", nil)
	plan.EvidenceIDs = []int64{fact, fact}
	if _, err := add(plan); err != nil {
		t.Fatal(err)
	}
	foreignFact, _ := other.AddNode(KindFact, map[string]any{"summary": "foreign"}, 0, "confirmed", "test", nil)
	plan.EvidenceIDs = []int64{foreignFact}
	if _, err := add(plan); err == nil {
		t.Fatal("foreign evidence admitted")
	}
	var orphans int
	if err := d.QueryRow(`SELECT COUNT(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'`, s.ID()).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 4 {
		t.Fatalf("unexpected intents after rejected admissions: %d", orphans)
	}
}

func TestRecoveryConcurrentAdmission(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	s := recoveryStore(t, d)
	root, _ := s.AddIntent(map[string]any{"summary": "initial"}, 5, nil, "")
	_ = s.SetNodeState(root, "exhausted")
	plan := RecoveryPlan{PreviousIntentID: root, Reason: "no_progress", Change: "Inspect the saved output with a new parser", ExpectedSignal: "Find a valid object or record an explicit negative result"}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.AddRecoveryIntent(map[string]any{"summary": "continued"}, 5, nil, plan)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if !strings.Contains(err.Error(), "已有相同") {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("admitted=%d want 1", success)
	}
}

func TestRecoveryErrorReadIsBoundedLocalAndErrorsOnly(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	s := recoveryStore(t, d)
	other := recoveryStore(t, d)
	id, _ := s.AddIntent(map[string]any{"summary": "local"}, 5, nil, "")
	foreign, _ := other.AddIntent(map[string]any{"summary": "foreign"}, 5, nil, "")
	appendError := func(store *ExplorationStore, id int64, kind string, isError bool) {
		t.Helper()
		_, err := store.AppendActivity(Activity{NodeID: &id, Kind: kind, Tool: "Bash", IsError: isError, Summary: strings.Repeat("中", 900), Detail: strings.Repeat("字", 3000)})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendError(other, foreign, "tool_result", true)
	appendError(s, id, "tool_result", false)
	appendError(s, id, "tool_use", true)
	for i := 0; i < 4; i++ {
		appendError(s, id, "tool_result", true)
	}
	items, err := s.RecentToolErrors(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || *items[0].NodeID != id || items[0].ID <= items[1].ID {
		t.Fatalf("%+v", items)
	}
	if len([]rune(items[0].Summary)) != 512 || len([]rune(items[0].Detail)) != 2048 {
		t.Fatal("error read not bounded")
	}
	all, err := s.RecentToolErrors(0)
	if err != nil || len(all) != 4 {
		t.Fatalf("len=%d err=%v", len(all), err)
	}
}
