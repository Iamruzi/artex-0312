package agent

import "runtime"

// Match norma v0.4.3 tool.shellCmd, rather than the shell used to start ARTEX.
func executionEnvironment() string { return shellEnvironment(runtime.GOOS) }

func shellEnvironment(goos string) string {
	if goos == "windows" {
		return "\n\n【本地命令执行环境】Bash 工具名称不代表解释器；当前宿主为 Windows，实际执行 powershell -NoProfile -NonInteractive -Command（Windows PowerShell）。不要使用 &&、||、Bash heredoc、export、head 或 /dev/null。用 PowerShell 原生语法；依赖前一步成功时显式检查 $LASTEXITCODE/异常，不能简单用分号掩盖失败。HTTP 调用明确使用 curl.exe，避免 curl 别名；复杂引号/JSON 先用 Write 写脚本或请求文件，再执行。每次 Bash 调用是独立进程，变量和 cd 不跨调用保留。此环境说明只适用于本地 Bash；远程 session_exec 的解释器由远端决定。解析错误/缺少命令属于本地执行问题，不能据此判定目标无漏洞。不要自动下载、安装或切换解释器来规避平台规则。"
	}
	return "\n\n【本地命令执行环境】Bash 工具在当前 Unix 宿主实际执行 bash -c；使用 Bash 语法，不要混入 PowerShell 的 $env:、Get-Content 或反引号换行。每次调用是独立进程，变量和 cd 不跨调用保留；不要假定额外工具已安装。此说明不代表远程 session_exec 的环境。解析错误/缺少命令属于本地执行问题，不能据此判定目标无漏洞。"
}

const recoveryRules = `

【失败恢复与续派】
graph_overview.recovery_memory 是当前任务工具错误的有界分类摘要，不是目标安全结论；按 intent_id/activity_id 查询原始上下文。没有错误记录不代表没有卡点。
本地 shell_syntax/command_missing 先修执行环境；budget 先读取上次产物；no_progress 换可检验的假设；policy_denied 是平台规则，禁止改写命令绕过，遵守限制并报告。每次续派已结束意图时使用 add_intent.recovery，给 previous_intent_id、reason、change、expected_signal；有实际新证据时给 evidence_ids。change 写具体改变，expected_signal 写下一步可观察且能证伪的结果，不用“继续尝试/加强探索”等空话。
旧意图仍 open/running 时不要续派；被人工停止/删除的意图不能自动复活。同一恢复链的相同改变且无不同证据会被拒绝；读取已有后续意图再决策。新方向可不带 recovery，禁止把续派伪装成新方向。Worker 应先看本意图 recovery 与全局错误摘要，沿用产物、避免重做；工作区、权限、目标范围与工具拦截继续按现有规则执行。`
