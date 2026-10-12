// Package tools 提供工具执行的谨慎性分析系统。
//
// ToolCautious 的设计哲学：不限制"能做什么"，而是提升"做什么之前先问问用户"的谨慎性。
// 用户不需要系统替他做安全决策，需要的是在敏感操作前得到清晰预警。
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// 风险决策结果
// ---------------------------------------------------------------------------

// ConfirmLevel 确认级别，决定是否需要用户确认以及确认的强度
type ConfirmLevel int

const (
	ConfirmNone   ConfirmLevel = iota // 无需确认，直接执行
	ConfirmNotify                     // 仅通知，不阻塞（用于低风险操作的通知）
	ConfirmNormal                     // 标准确认，需要用户同意
	ConfirmStrong                     // 强确认，需要用户明确同意，且提醒风险
	ConfirmBlock                      // 拒绝执行（用于极少数真正危险的操作，默认不存在）
)

// ConfirmDecision 对一次工具调用的谨慎性分析结果
type ConfirmDecision struct {
	Level    ConfirmLevel   // 确认级别
	Reasons  []string       // 触发预警的原因列表
	ToolName string         // 工具名称
	Params   map[string]any // 本次调用参数

	// 以下字段由调用方填充，用于后续记录
	Approved bool   // 用户是否批准
	UserNote string // 用户的备注（如"总是允许"）
}

// ReasonsString 返回原因列表的字符串表示
func (d *ConfirmDecision) ReasonsString() string {
	if len(d.Reasons) == 0 {
		return "无风险"
	}
	return strings.Join(d.Reasons, "; ")
}

// ---------------------------------------------------------------------------
// 参数风险评估器
// ---------------------------------------------------------------------------

// ParamRiskEvaluator 对单个参数进行风险评估
type ParamRiskEvaluator func(toolName string, paramName string, paramValue any) *RiskFinding

// RiskFinding 风险发现
type RiskFinding struct {
	Severity   ConfirmLevel // 风险严重程度
	Reason     string       // 风险描述
	Suggestion string       // 建议
}

// ---------------------------------------------------------------------------
// 敏感路径检测
// ---------------------------------------------------------------------------

// sensitivePattern 敏感路径模式
type sensitivePattern struct {
	pattern  string
	reason   string
	severity ConfirmLevel
}

// sensitivePatterns 敏感路径模式列表
// 匹配路径中包含这些模式的文件操作 → 需预警
var sensitivePatterns = []sensitivePattern{
	// 系统级敏感路径
	{"/etc/", "修改系统配置文件", ConfirmStrong},
	{"/etc/passwd", "修改用户账户文件", ConfirmBlock},
	{"/etc/shadow", "修改密码文件", ConfirmBlock},
	{"/etc/sudoers", "修改 sudo 权限配置", ConfirmBlock},
	{"/etc/ssh/", "修改 SSH 配置", ConfirmStrong},

	// 环境/密钥文件
	{".env", "修改环境变量文件", ConfirmStrong},
	{".env.local", "修改本地环境变量", ConfirmStrong},
	{".env.production", "修改生产环境变量", ConfirmStrong},
	{".env.prod", "修改生产环境变量", ConfirmStrong},

	// 配置文件
	{"config.yaml", "修改项目配置文件", ConfirmNormal},
	{"config.yml", "修改项目配置文件", ConfirmNormal},
	{"config.json", "修改项目配置文件", ConfirmNormal},
	{".gitconfig", "修改 Git 全局配置", ConfirmStrong},

	// 密钥/证书
	{"id_rsa", "修改私钥文件", ConfirmBlock},
	{"id_ed25519", "修改私钥文件", ConfirmBlock},
	{".pem", "修改证书文件", ConfirmStrong},
	{".key", "修改密钥文件", ConfirmStrong},

	// 项目关键文件
	{"go.mod", "修改模块依赖定义", ConfirmNormal},
	{"go.sum", "修改依赖校验文件", ConfirmNormal},
	{"package.json", "修改项目配置", ConfirmNormal},
	{"package-lock.json", "修改依赖锁定文件", ConfirmNormal},
	{"yarn.lock", "修改依赖锁定文件", ConfirmNormal},
	{"Cargo.toml", "修改 Rust 项目配置", ConfirmNormal},
	{"Cargo.lock", "修改依赖锁定文件", ConfirmNormal},
	{"pom.xml", "修改 Maven 项目配置", ConfirmNormal},
	{"build.gradle", "修改 Gradle 项目配置", ConfirmNormal},
	{"Makefile", "修改构建文件", ConfirmNormal},
	{"Dockerfile", "修改 Docker 构建文件", ConfirmNormal},
	{"docker-compose", "修改 Docker 编排配置", ConfirmNormal},

	// Shell 配置
	{".bashrc", "修改 Shell 配置", ConfirmStrong},
	{".zshrc", "修改 Shell 配置", ConfirmStrong},
	{".profile", "修改 Shell 配置", ConfirmStrong},
	{".bash_profile", "修改 Shell 配置", ConfirmStrong},

	// Git 相关
	{".git/", "修改 Git 内部文件", ConfirmStrong},

	// 系统关键路径
	{"/boot/", "修改系统引导文件", ConfirmBlock},
	{"/dev/", "修改设备文件", ConfirmBlock},
	{"/proc/", "修改系统进程文件", ConfirmBlock},

	// 项目根目录关键文件
	{"README.md", "修改项目说明文档", ConfirmNotify},
}

// ---------------------------------------------------------------------------
// 敏感命令检测
// ---------------------------------------------------------------------------

// commandPattern 危险命令模式
type commandPattern struct {
	pattern  string       // 匹配模式
	reason   string       // 风险描述
	severity ConfirmLevel // 风险等级
}

// dangerousCommands 危险命令模式列表
// 匹配命令中包含这些模式 → 需预警
var dangerousCommands = []commandPattern{
	// 删除操作
	{"rm -rf /", "删除根目录全部文件", ConfirmBlock},
	{"rm -fr /", "删除根目录全部文件", ConfirmBlock},
	{"rm -rf /*", "删除根目录全部文件", ConfirmBlock},
	{"rm -fr /*", "删除根目录全部文件", ConfirmBlock},
	{"rm -rf", "递归强制删除", ConfirmStrong},
	{"rm -fr", "递归强制删除", ConfirmStrong},
	{"rmdir", "删除目录", ConfirmNormal},

	// 危险权限
	{"chmod 777", "赋予所有用户全部权限", ConfirmStrong},
	{"chmod -R 777", "递归赋予所有用户全部权限", ConfirmStrong},
	{"chown", "修改文件所有者", ConfirmNormal},

	// 超级权限
	{"sudo ", "超级权限执行", ConfirmStrong},
	{"su ", "切换用户", ConfirmStrong},

	// 包管理（影响系统）
	{"apt install", "安装系统包", ConfirmStrong},
	{"apt remove", "移除系统包", ConfirmStrong},
	{"apt-get install", "安装系统包", ConfirmStrong},
	{"apt-get remove", "移除系统包", ConfirmStrong},
	{"dpkg ", "系统包管理操作", ConfirmStrong},
	{"pacman ", "安装系统包", ConfirmStrong},
	{"brew install", "安装 Homebrew 包", ConfirmNormal},
	{"brew uninstall", "卸载 Homebrew 包", ConfirmNormal},

	// 网络操作
	{"git push", "推送代码到远程仓库", ConfirmNormal},
	{"git push --force", "强制推送代码到远程仓库", ConfirmStrong},
	{"git push -f", "强制推送代码到远程仓库", ConfirmStrong},
	{"git reset --hard", "硬重置 Git 历史", ConfirmStrong},
	{"git branch -D", "强制删除分支", ConfirmNormal},
	{"git tag -d", "删除标签", ConfirmNormal},
	{"npm publish", "发布 npm 包", ConfirmStrong},
	{"npm unpublish", "撤销发布 npm 包", ConfirmStrong},
	{"pip install", "安装 Python 包", ConfirmNormal},
	{"pip uninstall", "卸载 Python 包", ConfirmNormal},

	// 危险写入
	{"> /dev/", "写入设备文件", ConfirmBlock},

	// Fork 炸弹
	{":(){ :|:& };:", "Fork 炸弹", ConfirmBlock},

	// 系统操作
	{"reboot", "重启系统", ConfirmStrong},
	{"shutdown", "关闭系统", ConfirmStrong},
	{"poweroff", "关闭电源", ConfirmStrong},
	{"halt", "停止系统", ConfirmStrong},
	{"init 0", "切换运行级别 0（关机）", ConfirmStrong},
	{"init 6", "切换运行级别 6（重启）", ConfirmStrong},
	{"systemctl ", "管理系统服务", ConfirmStrong},
	{"service ", "管理系统服务", ConfirmNormal},

	// 格式化/磁盘
	{"mkfs", "格式化磁盘", ConfirmBlock},
	{"fdisk", "磁盘分区操作", ConfirmBlock},
	{"dd if=", "磁盘低级操作", ConfirmBlock},
	{"parted", "磁盘分区操作", ConfirmBlock},

	// 数据库
	{"DROP DATABASE", "删除数据库", ConfirmStrong},
	{"DROP TABLE", "删除数据表", ConfirmStrong},
	{"TRUNCATE ", "清空数据表", ConfirmStrong},
	{"DELETE FROM", "删除数据（无 WHERE）", ConfirmNormal},
	{"drop database", "删除数据库", ConfirmStrong},
	{"drop table", "删除数据表", ConfirmStrong},
	{"truncate ", "清空数据表", ConfirmStrong},
	{"delete from", "删除数据（无 WHERE）", ConfirmNormal},

	// 远程连接
	{"ssh ", "远程连接", ConfirmNormal},
	{"scp ", "远程文件传输", ConfirmNormal},
	{"rsync", "远程同步", ConfirmNormal},
	{"wget ", "下载文件", ConfirmNormal},
	{"curl ", "网络请求", ConfirmNormal},

	// 容器
	{"docker rm", "删除容器", ConfirmNormal},
	{"docker rmi", "删除镜像", ConfirmNormal},
	{"docker system prune", "清理 Docker 系统", ConfirmStrong},
	{"docker exec -it", "进入容器交互模式", ConfirmNormal},
	{"kubectl delete", "删除 Kubernetes 资源", ConfirmStrong},

	// 脚本执行
	{"eval ", "动态执行字符串", ConfirmStrong},
	{"source ", "加载 Shell 脚本", ConfirmNormal},
	{"./configure", "运行配置脚本", ConfirmNormal},
}

// ---------------------------------------------------------------------------
// CautiousAnalyzer 谨慎性分析器
// ---------------------------------------------------------------------------

// CautiousMode 谨慎模式
type CautiousMode int

const (
	ModeQuiet    CautiousMode = iota // 安静模式：不预警，自动执行
	ModeStandard                     // 标准模式：仅敏感路径/命令预警（默认）
	ModeStrict                       // 严格模式：所有写操作和命令都确认
)

// String 返回模式名称
func (m CautiousMode) String() string {
	switch m {
	case ModeQuiet:
		return "quiet"
	case ModeStandard:
		return "standard"
	case ModeStrict:
		return "strict"
	default:
		return "unknown"
	}
}

// CautiousAnalyzer 谨慎性分析器
// 负责评估工具调用的风险，并给出确认建议。
// 不限制任何操作，只做风险评估和预警建议。
type CautiousAnalyzer struct {
	mode          CautiousMode
	customRules   []ParamRiskEvaluator
	alwaysAllowed map[string]bool // 用户已批准的"总是允许"记录
}

// NewCautiousAnalyzer 创建新的谨慎性分析器
func NewCautiousAnalyzer(mode CautiousMode) *CautiousAnalyzer {
	return &CautiousAnalyzer{
		mode:          mode,
		alwaysAllowed: make(map[string]bool),
	}
}

// SetMode 设置谨慎模式
func (ca *CautiousAnalyzer) SetMode(mode CautiousMode) {
	ca.mode = mode
}

// Mode 获取当前模式
func (ca *CautiousAnalyzer) Mode() CautiousMode {
	return ca.mode
}

// AddCustomRule 添加自定义参数风险评估规则
func (ca *CautiousAnalyzer) AddCustomRule(rule ParamRiskEvaluator) {
	ca.customRules = append(ca.customRules, rule)
}

// AddAlwaysAllowed 添加"总是允许"记录
func (ca *CautiousAnalyzer) AddAlwaysAllowed(toolName string, params map[string]any) {
	key := makeAllowKey(toolName, params)
	ca.alwaysAllowed[key] = true
}

// RemoveAlwaysAllowed 移除"总是允许"记录
func (ca *CautiousAnalyzer) RemoveAlwaysAllowed(toolName string, params map[string]any) {
	key := makeAllowKey(toolName, params)
	delete(ca.alwaysAllowed, key)
}

// IsAlwaysAllowed 检查是否已被用户批准"总是允许"
func (ca *CautiousAnalyzer) IsAlwaysAllowed(toolName string, params map[string]any) bool {
	if ca.mode == ModeQuiet {
		return true // 安静模式下所有操作都自动允许
	}
	key := makeAllowKey(toolName, params)
	return ca.alwaysAllowed[key]
}

// makeAllowKey 生成"总是允许"的键
func makeAllowKey(toolName string, params map[string]any) string {
	// 简化：只考虑工具名 + 主要参数（路径/命令）
	path, _ := params["path"].(string)
	command, _ := params["command"].(string)
	pattern, _ := params["pattern"].(string)
	url, _ := params["url"].(string)

	// 只取关键参数生成签名
	parts := []string{toolName}
	if path != "" {
		parts = append(parts, "path:"+path)
	}
	if command != "" {
		parts = append(parts, "cmd:"+command)
	}
	if pattern != "" {
		parts = append(parts, "pattern:"+pattern)
	}
	if url != "" {
		parts = append(parts, "url:"+url)
	}
	return strings.Join(parts, "|")
}

// Analyze 分析一次工具调用，返回确认决策
func (ca *CautiousAnalyzer) Analyze(toolName string, params map[string]any) *ConfirmDecision {
	dec := &ConfirmDecision{
		Level:    ConfirmNone,
		ToolName: toolName,
		Params:   params,
	}

	// 安静模式：不预警
	if ca.mode == ModeQuiet {
		return dec
	}

	// 检查是否已被用户批准"总是允许"
	if ca.IsAlwaysAllowed(toolName, params) {
		return dec
	}

	// 严格模式：所有写操作和命令都需要确认
	if ca.mode == ModeStrict {
		baseLevel := getBaseRiskLevel(toolName)
		if baseLevel >= RiskLow {
			dec.Level = ConfirmNormal
			dec.Reasons = append(dec.Reasons,
				fmt.Sprintf("严格模式: %s 需要确认", toolName))
		}
	}

	// 标准模式：具体分析参数风险
	switch toolName {
	case "write_file", "replace_in_file":
		ca.analyzeFileWrite(dec, params)
	case "exec_command":
		ca.analyzeCommand(dec, params)
	case "web_fetch":
		ca.analyzeWebFetch(dec, params)
	}

	// 应用自定义规则
	for _, rule := range ca.customRules {
		for paramName, paramValue := range params {
			finding := rule(toolName, paramName, paramValue)
			if finding != nil {
				if finding.Severity > dec.Level {
					dec.Level = finding.Severity
				}
				dec.Reasons = append(dec.Reasons, finding.Reason)
			}
		}
	}

	return dec
}

// analyzeFileWrite 分析文件写入操作的风险
func (ca *CautiousAnalyzer) analyzeFileWrite(dec *ConfirmDecision, params map[string]any) {
	path, _ := params["path"].(string)
	if path == "" {
		return
	}

	// 解析为绝对路径用于匹配
	absPath := path
	if !filepath.IsAbs(path) {
		wd, _ := os.Getwd()
		absPath = filepath.Join(wd, path)
	}
	absPath = filepath.Clean(absPath)

	// 检查敏感路径模式
	for _, sp := range sensitivePatterns {
		if strings.Contains(absPath, sp.pattern) || strings.Contains(path, sp.pattern) {
			if sp.severity > dec.Level {
				dec.Level = sp.severity
			}
			if sp.severity > ConfirmNone {
				dec.Reasons = append(dec.Reasons, sp.reason)
			}
		}
	}
}

// analyzeCommand 分析命令执行的风险
func (ca *CautiousAnalyzer) analyzeCommand(dec *ConfirmDecision, params map[string]any) {
	command, _ := params["command"].(string)
	if command == "" {
		return
	}

	// 检查危险命令模式
	for _, dc := range dangerousCommands {
		if commandContains(command, dc.pattern) {
			if dc.severity > dec.Level {
				dec.Level = dc.severity
			}
			dec.Reasons = append(dec.Reasons, dc.reason)
		}
	}

	// 检查管道/重定向（如果没有匹配到其他模式）
	if dec.Level == ConfirmNone {
		if strings.Contains(command, "|") || strings.Contains(command, ">") || strings.Contains(command, "<") {
			dec.Level = ConfirmNormal
			dec.Reasons = append(dec.Reasons, "包含管道/重定向操作")
		}
	}
}

// commandContains 检查命令是否包含指定模式，支持精确匹配避免误报。
// 对于以空格结尾的模式（如 "sudo "、"ssh "），要求模式匹配后紧跟空格或命令结束，
// 避免误匹配 "sudoku" 或 "sshfs"。
// 对于以 / 结尾的模式（如 "rm -rf /"），要求匹配后是空格、引号或命令结束，
// 避免误匹配 "rm -rf /tmp"。
func commandContains(command, pattern string) bool {
	// 对于以空格结尾的模式，使用更精确的匹配
	if strings.HasSuffix(pattern, " ") {
		trimmed := strings.TrimSuffix(pattern, " ")
		idx := strings.Index(command, trimmed)
		if idx < 0 {
			return false
		}
		after := idx + len(trimmed)
		// 匹配后必须是空格或命令结束（确保是完整单词）
		if after >= len(command) {
			return true
		}
		return command[after] == ' '
	}

	// 对于以 / 结尾的模式，检查边界
	if strings.HasSuffix(pattern, "/") {
		idx := strings.Index(command, pattern)
		if idx < 0 {
			return false
		}
		after := idx + len(pattern)
		if after >= len(command) {
			return true
		}
		nextChar := command[after]
		return nextChar == ' ' || nextChar == '\'' || nextChar == '"' ||
			nextChar == ';' || nextChar == '&' || nextChar == '|'
	}

	// 一般模式，直接包含匹配
	return strings.Contains(command, pattern)
}

// analyzeWebFetch 分析网络请求的风险
func (ca *CautiousAnalyzer) analyzeWebFetch(dec *ConfirmDecision, params map[string]any) {
	url, _ := params["url"].(string)
	if url == "" {
		return
	}

	// 检查是否是内部网络请求
	if strings.HasPrefix(url, "http://localhost") ||
		strings.HasPrefix(url, "http://127.0.0.1") ||
		strings.HasPrefix(url, "http://10.") ||
		strings.HasPrefix(url, "http://172.") ||
		strings.HasPrefix(url, "http://192.168.") ||
		strings.HasPrefix(url, "https://localhost") {
		dec.Level = ConfirmNormal
		dec.Reasons = append(dec.Reasons, "请求内部网络地址")
	}
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// getBaseRiskLevel 获取工具的基础风险等级（来自 registry 定义）
func getBaseRiskLevel(toolName string) RiskLevel {
	if level, ok := toolRiskLevels[toolName]; ok {
		return level
	}
	return RiskHigh
}

// CautiousSummary 返回当前谨慎性配置的摘要
func (ca *CautiousAnalyzer) CautiousSummary() string {
	return fmt.Sprintf("模式: %s, 自定义规则: %d, 已批准记录: %d",
		ca.mode.String(), len(ca.customRules), len(ca.alwaysAllowed))
}
