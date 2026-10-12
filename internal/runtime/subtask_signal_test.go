package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/tools"
)

// TestSpawnTask_EmitsSubTaskSignals 验证 SpawnTask 向 progressChan 发出 § 前缀的子任务面板信号。
// 这是 TUI 面板的数据来源——若此测试通过，说明 runtime→TUI 的信号链路代码正确，
// 子任务不显示的根因只能是"模型未调用 spawn_task 工具"。
func TestSpawnTask_EmitsSubTaskSignals(t *testing.T) {
	rt := newSpawnRuntime(t)
	progressCh := make(chan string, 128)
	ctx := tools.WithProgressChan(context.Background(), progressCh)

	result, err := rt.SpawnTask(ctx, "main", tools.TaskSpec{
		Task:      "实现登录函数",
		Directive: "CODE_AGENT",
	})
	if err != nil {
		t.Fatalf("SpawnTask 错误: %v", err)
	}
	if !strings.Contains(result, "已实现") {
		t.Fatalf("结果应含子 Agent 输出，got %q", result)
	}

	// 收集 progressCh 中的所有 § 信号（SpawnTask return 后 buffered channel 仍有未消费消息）
	var signals []string
	for {
		select {
		case m, ok := <-progressCh:
			if !ok {
				goto collected
			}
			signals = append(signals, m)
		default:
			goto collected
		}
	}
collected:
	if len(signals) == 0 {
		t.Fatal("progressCh 应收到 § 信号，实际为空——信号链路断裂")
	}

	// 验证三类信号都到达
	var gotStart, gotTok, gotDone bool
	for _, s := range signals {
		switch {
		case strings.HasPrefix(s, "§START|"):
			gotStart = true
			if !strings.Contains(s, "sub-1") {
				t.Errorf("§START 应含 subID，got %q", s)
			}
			if !strings.Contains(s, "CODE_AGENT") {
				t.Errorf("§START 应含 directive，got %q", s)
			}
			if !strings.Contains(s, "实现登录函数") {
				t.Errorf("§START 应含 task 描述，got %q", s)
			}
			// 新格式：§START|<subID>|<directive>|<employee>|<task>（5 段）
			// 装配修复后：directive 段保留原始通用指令（CODE_AGENT），员工段为
			// 真实装配的员工名（coder）——员工在干活（非仅显示）。
			parts := strings.Split(s, "|")
			if len(parts) == 5 {
				if parts[3] != "coder" {
					t.Errorf("§START 员工段应为 coder（CODE_AGENT 映射），got %q (parts=%v)", parts[3], parts)
				}
			} else {
				t.Errorf("§START 应为新格式 5 段（带员工段），got %q", s)
			}
		case strings.HasPrefix(s, "§TOK|"):
			gotTok = true
		case strings.HasPrefix(s, "§DONE|"):
			gotDone = true
		}
	}
	if !gotStart {
		t.Errorf("未收到 §START 信号，所有信号: %v", signals)
	}
	if !gotTok {
		t.Errorf("未收到 §TOK 信号（子 Agent token 转发），所有信号: %v", signals)
	}
	if !gotDone {
		t.Errorf("未收到 §DONE 信号，所有信号: %v", signals)
	}
	t.Logf("收到 %d 个 § 信号: start=%v tok=%v done=%v", len(signals), gotStart, gotTok, gotDone)
}
