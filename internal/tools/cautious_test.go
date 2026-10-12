package tools

import (
	"strings"
	"testing"
)

func TestCautiousAnalyzer_QuietMode(t *testing.T) {
	ca := NewCautiousAnalyzer(ModeQuiet)

	// 安静模式下，所有操作都不预警
	dec := ca.Analyze("exec_command", map[string]any{
		"command": "rm -rf /",
	})
	if dec.Level != ConfirmNone {
		t.Errorf("安静模式应返回 ConfirmNone，得到 %v", dec.Level)
	}
	if len(dec.Reasons) != 0 {
		t.Errorf("安静模式不应有原因，得到 %v", dec.Reasons)
	}
}

func TestCautiousAnalyzer_StandardMode_FileWrite(t *testing.T) {
	ca := NewCautiousAnalyzer(ModeStandard)

	tests := []struct {
		name     string
		path     string
		wantLvl  ConfirmLevel
		wantRisk bool // 是否包含风险原因
	}{
		{"普通文件", "test.txt", ConfirmNone, false},
		{"项目配置文件", "config.yaml", ConfirmNormal, true},
		{"环境变量", ".env", ConfirmStrong, true},
		{"私钥文件", "id_rsa", ConfirmBlock, true},
		{"系统配置", "/etc/hosts", ConfirmStrong, true},
		{"密码文件", "/etc/passwd", ConfirmBlock, true},
		{"shadow文件", "/etc/shadow", ConfirmBlock, true},
		{"sudoers", "/etc/sudoers", ConfirmBlock, true},
		{"SSH配置", "/etc/ssh/sshd_config", ConfirmStrong, true},
		{"README", "README.md", ConfirmNotify, true},
		{"Go模块", "go.mod", ConfirmNormal, true},
		{"Dockerfile", "Dockerfile", ConfirmNormal, true},
		{"Git配置", ".gitconfig", ConfirmStrong, true},
		{"Git内部", ".git/config", ConfirmStrong, true},
		{"Shell配置", ".bashrc", ConfirmStrong, true},
		{"证书", "server.pem", ConfirmStrong, true},
		{"密钥", "private.key", ConfirmStrong, true},
		{"系统引导", "/boot/grub.cfg", ConfirmBlock, true},
		{"设备文件", "/dev/sda", ConfirmBlock, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := ca.Analyze("write_file", map[string]any{
				"path": tt.path,
			})
			if dec.Level != tt.wantLvl {
				t.Errorf("级别: 期望 %v, 得到 %v (原因: %v)", tt.wantLvl, dec.Level, dec.Reasons)
			}
			if tt.wantRisk && len(dec.Reasons) == 0 {
				t.Errorf("应包含风险原因，但没有")
			}
			if !tt.wantRisk && len(dec.Reasons) > 0 {
				t.Errorf("不应有风险原因，但得到 %v", dec.Reasons)
			}
		})
	}
}

func TestCautiousAnalyzer_StandardMode_Command(t *testing.T) {
	ca := NewCautiousAnalyzer(ModeStandard)

	tests := []struct {
		name     string
		command  string
		wantLvl  ConfirmLevel
		wantRisk bool
	}{
		{"安全命令", "go build", ConfirmNone, false},
		{"安全命令2", "ls -la", ConfirmNone, false},
		{"安全命令3", "cat main.go", ConfirmNone, false},
		{"递归删除", "rm -rf /tmp/old", ConfirmStrong, true},
		{"删除根目录", "rm -rf /", ConfirmBlock, true},
		{"权限修改", "chmod 777 script.sh", ConfirmStrong, true},
		{"超级权限", "sudo apt update", ConfirmStrong, true},
		{"Git推送", "git push origin main", ConfirmNormal, true},
		{"强制推送", "git push --force origin main", ConfirmStrong, true},
		{"硬重置", "git reset --hard HEAD~1", ConfirmStrong, true},
		{"npm发布", "npm publish", ConfirmStrong, true},
		{"系统重启", "reboot", ConfirmStrong, true},
		{"关机", "shutdown -h now", ConfirmStrong, true},
		{"格式化", "mkfs.ext4 /dev/sdb1", ConfirmBlock, true},
		{"删除数据库", "DROP DATABASE testdb", ConfirmStrong, true},
		{"删除表", "DROP TABLE users", ConfirmStrong, true},
		{"清空表", "TRUNCATE TABLE logs", ConfirmStrong, true},
		{"Fork炸弹", ":(){ :|:& };:", ConfirmBlock, true},
		{"管道操作", "cat file | grep pattern", ConfirmNormal, true},
		{"重定向", "echo test > file.txt", ConfirmNormal, true},
		{"brew安装", "brew install node", ConfirmNormal, true},
		{"pip安装", "pip install requests", ConfirmNormal, true},
		{"docker删除", "docker rm container1", ConfirmNormal, true},
		{"docker清理", "docker system prune -a", ConfirmStrong, true},
		{"kubectl删除", "kubectl delete pod nginx", ConfirmStrong, true},
		{"源码编译", "./configure && make", ConfirmNormal, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := ca.Analyze("exec_command", map[string]any{
				"command": tt.command,
			})
			if dec.Level != tt.wantLvl {
				t.Errorf("级别: 期望 %v, 得到 %v (命令: %q, 原因: %v)", tt.wantLvl, dec.Level, tt.command, dec.Reasons)
			}
			if tt.wantRisk && len(dec.Reasons) == 0 {
				t.Errorf("应包含风险原因，但没有 (命令: %q)", tt.command)
			}
			if !tt.wantRisk && len(dec.Reasons) > 0 {
				t.Errorf("不应有风险原因，但得到 %v (命令: %q)", dec.Reasons, tt.command)
			}
		})
	}
}

func TestCautiousAnalyzer_StrictMode(t *testing.T) {
	ca := NewCautiousAnalyzer(ModeStrict)

	// 严格模式下，所有写操作和命令都需要确认
	tests := []struct {
		name     string
		toolName string
		params   map[string]any
		wantLvl  ConfirmLevel
	}{
		{"只读操作", "read_file", map[string]any{"path": "test.txt"}, ConfirmNone},
		{"列出文件", "list_files", map[string]any{"path": "."}, ConfirmNone},
		{"搜索内容", "search_content", map[string]any{"pattern": "test"}, ConfirmNone},
		{"Git diff", "git_diff", map[string]any{}, ConfirmNone},
		{"网络请求", "web_fetch", map[string]any{"url": "https://example.com"}, ConfirmNormal},
		{"写文件", "write_file", map[string]any{"path": "test.txt"}, ConfirmNormal},
		{"替换文件", "replace_in_file", map[string]any{"path": "test.go"}, ConfirmNormal},
		{"运行测试", "run_tests", map[string]any{"test_target": "./..."}, ConfirmNormal},
		{"执行命令", "exec_command", map[string]any{"command": "ls"}, ConfirmNormal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := ca.Analyze(tt.toolName, tt.params)
			if dec.Level != tt.wantLvl {
				t.Errorf("级别: 期望 %v, 得到 %v (原因: %v)", tt.wantLvl, dec.Level, dec.Reasons)
			}
		})
	}
}

func TestCautiousAnalyzer_AlwaysAllowed(t *testing.T) {
	ca := NewCautiousAnalyzer(ModeStandard)

	params := map[string]any{"path": "/etc/config.yaml"}

	// 初始不应被允许
	if ca.IsAlwaysAllowed("write_file", params) {
		t.Error("初始状态下不应被允许")
	}

	// 分析应返回预警
	dec := ca.Analyze("write_file", params)
	if dec.Level == ConfirmNone {
		t.Error("敏感路径应产生预警")
	}

	// 添加"总是允许"
	ca.AddAlwaysAllowed("write_file", params)
	if !ca.IsAlwaysAllowed("write_file", params) {
		t.Error("添加后应被允许")
	}

	// 再次分析应无预警
	dec = ca.Analyze("write_file", params)
	if dec.Level != ConfirmNone {
		t.Errorf("总是允许后应无预警，得到 %v", dec.Level)
	}

	// 移除后应恢复预警
	ca.RemoveAlwaysAllowed("write_file", params)
	if ca.IsAlwaysAllowed("write_file", params) {
		t.Error("移除后不应被允许")
	}

	dec = ca.Analyze("write_file", params)
	if dec.Level == ConfirmNone {
		t.Error("移除后应恢复预警")
	}
}

func TestCautiousAnalyzer_WebFetch(t *testing.T) {
	ca := NewCautiousAnalyzer(ModeStandard)

	tests := []struct {
		name    string
		url     string
		wantLvl ConfirmLevel
	}{
		{"外部URL", "https://example.com", ConfirmNone},
		{"外部URL2", "https://api.github.com", ConfirmNone},
		{"本地", "http://localhost:8080/api", ConfirmNormal},
		{"本地IP", "http://127.0.0.1:3000", ConfirmNormal},
		{"内网10", "http://10.0.0.1/admin", ConfirmNormal},
		{"内网172", "http://172.16.0.1/config", ConfirmNormal},
		{"内网192", "http://192.168.1.1/setup", ConfirmNormal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := ca.Analyze("web_fetch", map[string]any{
				"url": tt.url,
			})
			if dec.Level != tt.wantLvl {
				t.Errorf("级别: 期望 %v, 得到 %v (URL: %q, 原因: %v)", tt.wantLvl, dec.Level, tt.url, dec.Reasons)
			}
		})
	}
}

func TestCautiousAnalyzer_CustomRules(t *testing.T) {
	ca := NewCautiousAnalyzer(ModeStandard)

	// 添加自定义规则：任何对 main.go 的写入都要强确认
	ca.AddCustomRule(func(toolName string, paramName string, paramValue any) *RiskFinding {
		if toolName == "write_file" && paramName == "path" {
			if path, ok := paramValue.(string); ok && path == "main.go" {
				return &RiskFinding{
					Severity:   ConfirmStrong,
					Reason:     "自定义规则: 修改主入口文件",
					Suggestion: "请确认是否修改主入口文件",
				}
			}
		}
		return nil
	})

	dec := ca.Analyze("write_file", map[string]any{"path": "main.go"})
	if dec.Level != ConfirmStrong {
		t.Errorf("自定义规则应返回 ConfirmStrong，得到 %v", dec.Level)
	}
	if len(dec.Reasons) == 0 {
		t.Error("应包含自定义规则的原因")
	}

	// 其他文件不受影响
	dec2 := ca.Analyze("write_file", map[string]any{"path": "utils.go"})
	if dec2.Level != ConfirmNone {
		t.Errorf("自定义规则不应影响其他文件，得到 %v", dec2.Level)
	}
}

func TestCautiousAnalyzer_ReplaceInFile(t *testing.T) {
	ca := NewCautiousAnalyzer(ModeStandard)

	// replace_in_file 应和 write_file 一样检查敏感路径
	dec := ca.Analyze("replace_in_file", map[string]any{
		"path":    "/etc/ssh/sshd_config",
		"old_str": "PermitRootLogin yes",
		"new_str": "PermitRootLogin no",
	})
	if dec.Level != ConfirmStrong {
		t.Errorf("修改 SSH 配置应返回 ConfirmStrong，得到 %v (原因: %v)", dec.Level, dec.Reasons)
	}
}

func TestCautiousAnalyzer_ModeSwitch(t *testing.T) {
	ca := NewCautiousAnalyzer(ModeStandard)

	// 标准模式：敏感命令预警
	dec := ca.Analyze("exec_command", map[string]any{"command": "rm -rf /tmp"})
	if dec.Level != ConfirmStrong {
		t.Errorf("标准模式应预警 rm -rf，得到 %v", dec.Level)
	}

	// 切换到安静模式
	ca.SetMode(ModeQuiet)
	dec = ca.Analyze("exec_command", map[string]any{"command": "rm -rf /tmp"})
	if dec.Level != ConfirmNone {
		t.Errorf("安静模式不应预警，得到 %v", dec.Level)
	}

	// 切回标准模式
	ca.SetMode(ModeStandard)
	dec = ca.Analyze("exec_command", map[string]any{"command": "rm -rf /tmp"})
	if dec.Level != ConfirmStrong {
		t.Errorf("切回标准模式后应预警，得到 %v", dec.Level)
	}
}

func TestCautiousAnalyzer_EdgeCases(t *testing.T) {
	ca := NewCautiousAnalyzer(ModeStandard)

	// 空参数
	dec := ca.Analyze("write_file", map[string]any{})
	if dec.Level != ConfirmNone {
		t.Errorf("空参数不应预警，得到 %v", dec.Level)
	}

	// 空路径
	dec = ca.Analyze("write_file", map[string]any{"path": ""})
	if dec.Level != ConfirmNone {
		t.Errorf("空路径不应预警，得到 %v", dec.Level)
	}

	// 未知工具
	dec = ca.Analyze("unknown_tool", map[string]any{"path": "/etc/config"})
	if dec.Level != ConfirmNone {
		t.Errorf("未知工具不应预警，得到 %v", dec.Level)
	}

	// 空命令
	dec = ca.Analyze("exec_command", map[string]any{"command": ""})
	if dec.Level != ConfirmNone {
		t.Errorf("空命令不应预警，得到 %v", dec.Level)
	}
}

func TestMakeAllowKey(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		params   map[string]any
		want     string
	}{
		{"文件路径", "write_file", map[string]any{"path": "/etc/config.yaml"}, "write_file|path:/etc/config.yaml"},
		{"命令", "exec_command", map[string]any{"command": "rm -rf /tmp"}, "exec_command|cmd:rm -rf /tmp"},
		{"搜索", "search_content", map[string]any{"pattern": "TODO"}, "search_content|pattern:TODO"},
		{"URL", "web_fetch", map[string]any{"url": "https://example.com"}, "web_fetch|url:https://example.com"},
		{"混合参数", "write_file", map[string]any{"path": "/a/b", "content": "hello"}, "write_file|path:/a/b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := makeAllowKey(tt.toolName, tt.params)
			if got != tt.want {
				t.Errorf("期望 %q, 得到 %q", tt.want, got)
			}
		})
	}
}

func TestConfirmDecision_ReasonsString(t *testing.T) {
	dec := &ConfirmDecision{Level: ConfirmNone, Reasons: nil}
	if dec.ReasonsString() != "无风险" {
		t.Errorf("空原因应返回 '无风险'，得到 %q", dec.ReasonsString())
	}

	dec.Reasons = []string{"原因1", "原因2"}
	if dec.ReasonsString() != "原因1; 原因2" {
		t.Errorf("期望 '原因1; 原因2'，得到 %q", dec.ReasonsString())
	}
}

func TestCautiousMode_String(t *testing.T) {
	if ModeQuiet.String() != "quiet" {
		t.Errorf("期望 quiet，得到 %s", ModeQuiet.String())
	}
	if ModeStandard.String() != "standard" {
		t.Errorf("期望 standard，得到 %s", ModeStandard.String())
	}
	if ModeStrict.String() != "strict" {
		t.Errorf("期望 strict，得到 %s", ModeStrict.String())
	}
}

func TestCautiousAnalyzer_ReplaceInFile_NoMatch(t *testing.T) {
	ca := NewCautiousAnalyzer(ModeStandard)

	// 普通文件的 replace_in_file 不应预警
	dec := ca.Analyze("replace_in_file", map[string]any{
		"path":    "normal_file.go",
		"old_str": "old",
		"new_str": "new",
	})
	if dec.Level != ConfirmNone {
		t.Errorf("普通文件 replace 不应预警，得到 %v (原因: %v)", dec.Level, dec.Reasons)
	}
}

func TestCautiousAnalyzer_Command_NoMatch(t *testing.T) {
	ca := NewCautiousAnalyzer(ModeStandard)

	// 简单命令不应预警
	commands := []string{
		"go build ./...",
		"go test ./...",
		"go fmt ./...",
		"git status",
		"git log --oneline",
		"git diff",
		"ls -la",
		"cat file.go",
		"pwd",
		"echo hello",
		"date",
		"whoami",
		"which go",
		"go version",
		"make build",
		"npm run build",
		"cargo build",
		"python script.py",
		"node app.js",
		"deno run main.ts",
		"bun run index.ts",
	}

	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			dec := ca.Analyze("exec_command", map[string]any{"command": cmd})
			if dec.Level != ConfirmNone {
				t.Errorf("命令 %q 不应预警，得到 %v (原因: %v)", cmd, dec.Level, dec.Reasons)
			}
		})
	}
}

func TestCautiousAnalyzer_SensitivePaths_Detailed(t *testing.T) {
	ca := NewCautiousAnalyzer(ModeStandard)

	// 更详细的路径测试
	tests := []struct {
		path    string
		wantLvl ConfirmLevel
		reason  string // 期望包含的原因关键词
	}{
		{".env", ConfirmStrong, "环境变量"},
		{".env.local", ConfirmStrong, "环境变量"},
		{".env.production", ConfirmStrong, "环境变量"},
		{".env.prod", ConfirmStrong, "环境变量"},
		{"config.yaml", ConfirmNormal, "配置文件"},
		{"config.yml", ConfirmNormal, "配置文件"},
		{"config.json", ConfirmNormal, "配置文件"},
		{".gitconfig", ConfirmStrong, "Git 全局配置"},
		{".git/config", ConfirmStrong, "Git 内部"},
		{"path/to/.git/config", ConfirmStrong, "Git 内部"},
		{".bashrc", ConfirmStrong, "Shell 配置"},
		{".zshrc", ConfirmStrong, "Shell 配置"},
		{".profile", ConfirmStrong, "Shell 配置"},
		{".bash_profile", ConfirmStrong, "Shell 配置"},
		{"id_rsa", ConfirmBlock, "私钥"},
		{"id_ed25519", ConfirmBlock, "私钥"},
		{"keys/server.pem", ConfirmStrong, "证书"},
		{"keys/private.key", ConfirmStrong, "密钥"},
		{"go.mod", ConfirmNormal, "模块"},
		{"go.sum", ConfirmNormal, "依赖"},
		{"package.json", ConfirmNormal, "项目配置"},
		{"Dockerfile", ConfirmNormal, "Docker"},
		{"docker-compose.yml", ConfirmNormal, "Docker"},
		{"Makefile", ConfirmNormal, "构建"},
		{"README.md", ConfirmNotify, ""},
		{"src/main.go", ConfirmNone, ""},
		{"docs/index.html", ConfirmNone, ""},
		{"/etc/passwd", ConfirmBlock, "用户账户"},
		{"/etc/shadow", ConfirmBlock, "密码"},
		{"/etc/sudoers", ConfirmBlock, "sudo"},
		{"/etc/ssh/sshd_config", ConfirmStrong, "SSH"},
		{"/boot/vmlinuz", ConfirmBlock, "系统引导"},
		{"/dev/sda", ConfirmBlock, "设备文件"},
		{"/proc/cpuinfo", ConfirmBlock, "系统进程"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			dec := ca.Analyze("write_file", map[string]any{"path": tt.path})
			if dec.Level != tt.wantLvl {
				t.Errorf("级别: 期望 %v, 得到 %v (原因: %v)", tt.wantLvl, dec.Level, dec.Reasons)
			}
			if tt.reason != "" && len(dec.Reasons) > 0 {
				found := false
				for _, r := range dec.Reasons {
					if strings.Contains(r, tt.reason) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("原因应包含 %q，但得到 %v", tt.reason, dec.Reasons)
				}
			}
		})
	}
}
