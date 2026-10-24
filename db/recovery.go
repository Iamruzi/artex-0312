package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// RecoveryPlan is optional continuation metadata, persisted in the intent JSON.
// RootIntentID and Fingerprint are always derived by admission, never trusted input.
type RecoveryPlan struct {
	PreviousIntentID int64   `json:"previous_intent_id"`
	Reason           string  `json:"reason"`
	Change           string  `json:"change"`
	ExpectedSignal   string  `json:"expected_signal"`
	EvidenceIDs      []int64 `json:"evidence_ids,omitempty"`
	RootIntentID     int64   `json:"root_intent_id,omitempty"`
	Fingerprint      string  `json:"fingerprint,omitempty"`
}

func (p RecoveryPlan) Validate() error {
	if p.PreviousIntentID <= 0 {
		return fmt.Errorf("recovery.previous_intent_id 必须是已结束的本任务意图 id")
	}
	switch p.Reason {
	case "shell_syntax", "command_missing", "tool_error", "budget", "no_progress", "policy_denied", "new_evidence":
	default:
		return fmt.Errorf("recovery.reason 必须为 shell_syntax/command_missing/tool_error/budget/no_progress/policy_denied/new_evidence")
	}
	for name, text := range map[string]string{"change": p.Change, "expected_signal": p.ExpectedSignal} {
		n := utf8.RuneCountInString(strings.TrimSpace(text))
		if n < 8 || n > 600 {
			return fmt.Errorf("recovery.%s 需 8–600 字符：说明具体改变与可观察结果", name)
		}
	}
	if len(p.EvidenceIDs) > 8 {
		return fmt.Errorf("recovery.evidence_ids 最多 8 个本任务事实/发现")
	}
	return nil
}

// RecentToolErrors reads at most 200 errors, not the unbounded activity transcript.
// Only local intent records are eligible, even when another task is a direct source.
func (s *ExplorationStore) RecentToolErrors(limit int) ([]Activity, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT a.id, a.node_id, COALESCE(a.tool,''),
LEFT(COALESCE(a.summary,''),512), LEFT(COALESCE(a.detail,''),2048)
FROM activity a JOIN exploration_nodes n ON n.id=a.node_id AND n.exploration_id=a.exploration_id
WHERE a.exploration_id=$1 AND n.kind='intent' AND a.kind='tool_result' AND a.is_error
ORDER BY a.id DESC LIMIT $2`, s.expID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.NodeID, &a.Tool, &a.Summary, &a.Detail); err != nil {
			return nil, err
		}
		a.IsError = true
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddRecoveryIntent serializes one recovery family on its root row. No schema
// migration or process-local cache is needed; concurrent planners cannot admit
// duplicate declared changes. This is exact normalization, not semantic matching.
func (s *ExplorationStore) AddRecoveryIntent(payload map[string]any, priority int, anchors []int64, plan RecoveryPlan) (int64, error) {
	if err := plan.Validate(); err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var previous struct {
		Recovery RecoveryPlan `json:"recovery"`
	}
	var raw []byte
	err = tx.QueryRow(`SELECT payload FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent'`, plan.PreviousIntentID, s.expID).Scan(&raw)
	if err != nil {
		return 0, fmt.Errorf("recovery 来源不存在于本任务: %w", err)
	}
	if err := json.Unmarshal(raw, &previous); err != nil {
		return 0, err
	}
	plan.RootIntentID = previous.Recovery.RootIntentID
	if plan.RootIntentID <= 0 {
		plan.RootIntentID = plan.PreviousIntentID
	}
	var rootID int64
	if err := tx.QueryRow(`SELECT id FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent' FOR UPDATE`, plan.RootIntentID, s.expID).Scan(&rootID); err != nil {
		return 0, err
	}
	var state string
	if err := tx.QueryRow(`SELECT state FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 FOR SHARE`, plan.PreviousIntentID, s.expID).Scan(&state); err != nil {
		return 0, err
	}
	if state != "done" && state != "blocked" && state != "exhausted" {
		return 0, fmt.Errorf("recovery 来源状态 %s 不允许续派；不得重复正在运行/排队的意图或复活人工停止项", state)
	}
	ids := append([]int64(nil), plan.EvidenceIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	plan.EvidenceIDs = nil
	for _, id := range ids {
		if len(plan.EvidenceIDs) > 0 && plan.EvidenceIDs[len(plan.EvidenceIDs)-1] == id {
			continue
		}
		var kind string
		if err := tx.QueryRow(`SELECT kind FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 FOR SHARE`, id, s.expID).Scan(&kind); err != nil || (kind != KindFact && kind != KindFinding) {
			return 0, fmt.Errorf("recovery.evidence_id %d 不是本任务的事实/发现", id)
		}
		plan.EvidenceIDs = append(plan.EvidenceIDs, id)
	}
	if plan.Reason == "new_evidence" && len(plan.EvidenceIDs) == 0 {
		return 0, fmt.Errorf("new_evidence 必须引用实际 evidence_ids")
	}
	plan.Change = strings.TrimSpace(plan.Change)
	plan.ExpectedSignal = strings.TrimSpace(plan.ExpectedSignal)
	key, _ := json.Marshal([]any{strings.ToLower(strings.Join(strings.Fields(plan.Change), " ")), plan.EvidenceIDs})
	sum := sha256.Sum256(key)
	plan.Fingerprint = hex.EncodeToString(sum[:])
	var duplicate int64
	err = tx.QueryRow(`SELECT id FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'
AND payload->'recovery'->>'root_intent_id'=$2 AND payload->'recovery'->>'fingerprint'=$3 LIMIT 1`, s.expID, fmt.Sprint(rootID), plan.Fingerprint).Scan(&duplicate)
	if err == nil {
		return 0, fmt.Errorf("恢复链已有相同改变与证据的意图 #%d；先看其产物，提供不同改变或实际新证据", duplicate)
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	copyPayload := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		copyPayload[k] = v
	}
	copyPayload["recovery"] = plan
	encoded, err := json.Marshal(copyPayload)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin)
VALUES ($1,'intent',$2,$3,'open','planner') RETURNING id`, s.expID, string(jsonbClean(encoded)), priority).Scan(&id); err != nil {
		return 0, err
	}
	for _, a := range anchors {
		if _, err := tx.Exec(`INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, a); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}
