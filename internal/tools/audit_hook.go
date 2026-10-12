package tools

import (
	"sync"

	"github.com/contextloom/contextloom-agent/internal/audit"
)

// auditMu 保护全局审计器（工具包全局单例注入点）。
var (
	auditMu   sync.RWMutex
	auditor   *audit.Auditor
	auditSess func() string // 获取当前会话 ID（由 runtime 注入）
)

// SetAuditor 注入全局审计器（runtime 启动时调用；nil 可关闭）。
// 审计器记录所有工具执行（成功/失败），对标企业治理审计追踪。
func SetAuditor(a *audit.Auditor) {
	auditMu.Lock()
	defer auditMu.Unlock()
	auditor = a
}

// SetAuditSessionProvider 注入会话 ID 提供函数（可选）。
func SetAuditSessionProvider(fn func() string) {
	auditMu.Lock()
	defer auditMu.Unlock()
	auditSess = fn
}

// auditActionFor 工具名 → 审计动作映射。
func auditActionFor(tool string) audit.Action {
	switch tool {
	case "write_file":
		return audit.ActionWriteFile
	case "replace_in_file":
		return audit.ActionReplaceFile
	case "exec_command":
		return audit.ActionExecCommand
	case "read_file":
		return audit.ActionReadFile
	case "list_files":
		return audit.ActionListFiles
	default:
		return audit.ActionToolCall
	}
}

// auditPath 从工具参数中提取路径字段（尽力而为）。
func auditPath(params map[string]any) string {
	for _, k := range []string{"path", "file", "target"} {
		if v, ok := params[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// errString 错误信息（截断 300 字符，防审计日志膨胀）。
func errString(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

// recordAudit 记录一次工具执行审计（nil 审计器时为空操作，不阻塞主流程）。
// 在工具执行完成后由 registry.Execute 调用。
func recordAudit(action audit.Action, tool, path string, ok bool, ms int64, errMsg string) {
	auditMu.RLock()
	a := auditor
	sess := ""
	if auditSess != nil {
		sess = auditSess()
	}
	auditMu.RUnlock()
	if a == nil {
		return
	}
	a.Record(audit.Entry{
		Session: sess,
		Action:  action,
		Tool:    tool,
		Path:    path,
		OK:      ok,
		Ms:      ms,
		Err:     errMsg,
	})
}
