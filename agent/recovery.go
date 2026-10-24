package agent

import (
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

func recoverySchema() map[string]any {
	schema := obj(map[string]any{
		"previous_intent_id": idp("已结束的本任务意图 id；不得续派仍 open/running 或人工停止的意图"),
		"reason":             map[string]any{"type": "string", "enum": []string{"shell_syntax", "command_missing", "tool_error", "budget", "no_progress", "policy_denied", "new_evidence"}},
		"change":             str("8–600 字符，本次具体改变什么；禁止用继续尝试代替诊断，禁止规避平台拦截"),
		"expected_signal":    str("8–600 字符，下一步预计观察什么、何时认定无进展"),
		"evidence_ids":       map[string]any{"type": "array", "maxItems": 8, "items": idp("实际存在的本任务事实/发现 id；new_evidence 时必填")},
	}, "previous_intent_id", "reason", "change", "expected_signal")
	schema["description"] = "续派/换路已尝试意图时填写。记录失败原因、实际改变与可观察信号；恢复链内相同改变且无不同证据会被拒绝。全新方向可省略。"
	return schema
}

// Only fixed labels and trace references enter prompts, never raw error output.
func recoveryCategory(tool, text string) string {
	s := strings.ToLower(text)
	if strings.Contains(s, "blocked by hook:") || strings.Contains(s, "【artex 平台管控") {
		return "policy_denied"
	}
	if tool == "Bash" {
		for _, marker := range []string{"parsererror", "unexpectedtoken", "not a valid statement separator", "不是此版本中的有效语句分隔符", "syntax error near unexpected token", "missing ')' in method call", "terminator expected at end of string"} {
			if strings.Contains(s, marker) {
				return "shell_syntax"
			}
		}
		if strings.Contains(s, "commandnotfoundexception") || strings.Contains(s, "command not found") || strings.Contains(s, "is not recognized as the name of a cmdlet") {
			return "command_missing"
		}
	}
	return "tool_error"
}

func recoveryMemory(items []db.Activity) []map[string]any {
	out := make([]map[string]any, 0)
	index := map[string]int{}
	for _, a := range items {
		if a.NodeID == nil || !a.IsError {
			continue
		}
		category := recoveryCategory(a.Tool, a.Summary+"\n"+a.Detail)
		// Fixed-size keys; a tool name is metadata, never copied from tool stdout.
		key := strings.Join([]string{strconv.FormatInt(*a.NodeID, 10), a.Tool, category}, "|")
		if i, ok := index[key]; ok {
			out[i]["count"] = out[i]["count"].(int) + 1
			continue
		}
		if len(out) >= 20 {
			continue
		}
		index[key] = len(out)
		out = append(out, map[string]any{"intent_id": *a.NodeID, "tool": a.Tool, "category": category, "count": 1, "activity_id": a.ID})
	}
	return out
}
