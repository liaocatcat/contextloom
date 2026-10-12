package tools

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

func TestHook_PreReject(t *testing.T) {
	r := NewRegistry()
	r.AddHook(HookFunc{
		Pre: func(ctx context.Context, call types.ToolCall) (types.ToolCall, bool, string) {
			if call.Function.Name == "exec_command" && strings.Contains(call.Function.Arguments, "rm -rf") {
				return call, false, "禁止 rm -rf"
			}
			return call, true, ""
		},
	})
	res, _ := r.Execute(context.Background(), types.ToolCall{
		ID: "1",
		Function: types.FunctionCall{
			Name:      "exec_command",
			Arguments: `{"command":"rm -rf /"}`,
		},
	})
	if res == nil || res.Error == "" || !strings.Contains(res.Error, "拦截") {
		t.Errorf("Pre 拒绝应返回拦截错误: %+v", res)
	}
}

func TestHook_PreModifyParams(t *testing.T) {
	r := NewRegistry()
	r.AddHook(HookFunc{
		Pre: func(ctx context.Context, call types.ToolCall) (types.ToolCall, bool, string) {
			// 把 command 改成 echo safe
			call.Function.Arguments = `{"command":"echo safe"}`
			return call, true, ""
		},
	})
	res, _ := r.Execute(context.Background(), types.ToolCall{
		ID: "1",
		Function: types.FunctionCall{
			Name:      "exec_command",
			Arguments: `{"command":"echo original"}`,
		},
	})
	if res == nil || res.Error != "" {
		t.Fatalf("执行失败: %+v", res)
	}
	m := res.Result.(map[string]any)
	if !strings.Contains(m["stdout"].(string), "safe") {
		t.Errorf("参数修改未生效: %v", m["stdout"])
	}
}

func TestHook_PostModifyResult(t *testing.T) {
	r := NewRegistry()
	r.AddHook(HookFunc{
		Post: func(ctx context.Context, call types.ToolCall, result *types.ToolResult) *types.ToolResult {
			if result != nil && result.Error == "" {
				result.Error = "post-标记"
			}
			return result
		},
	})
	res, _ := r.Execute(context.Background(), types.ToolCall{
		ID: "1",
		Function: types.FunctionCall{
			Name:      "exec_command",
			Arguments: `{"command":"echo hi"}`,
		},
	})
	if res == nil || res.Error != "post-标记" {
		t.Errorf("Post 修改结果未生效: %+v", res)
	}
}

func TestHook_PostOnError(t *testing.T) {
	r := NewRegistry()
	var postCalls atomic.Int32
	r.AddHook(HookFunc{
		Post: func(ctx context.Context, call types.ToolCall, result *types.ToolResult) *types.ToolResult {
			postCalls.Add(1)
			return result
		},
	})
	// 执行失败工具（read_file 不存在的文件）
	res, _ := r.Execute(context.Background(), types.ToolCall{
		ID: "1",
		Function: types.FunctionCall{
			Name:      "read_file",
			Arguments: `{"path":"/no/such/file/xyz.go"}`,
		},
	})
	if res == nil || res.Error == "" {
		t.Fatalf("应失败: %+v", res)
	}
	if postCalls.Load() != 1 {
		t.Error("Post 应在失败时也调用")
	}
}

func TestHook_ChainOrder(t *testing.T) {
	r := NewRegistry()
	var order []string
	mk := func(name string) Hook {
		return HookFunc{
			Pre: func(ctx context.Context, call types.ToolCall) (types.ToolCall, bool, string) {
				order = append(order, "pre-"+name)
				return call, true, ""
			},
			Post: func(ctx context.Context, call types.ToolCall, result *types.ToolResult) *types.ToolResult {
				order = append(order, "post-"+name)
				return result
			},
		}
	}
	r.AddHook(mk("a"))
	r.AddHook(mk("b"))
	r.Execute(context.Background(), types.ToolCall{
		ID: "1",
		Function: types.FunctionCall{
			Name:      "exec_command",
			Arguments: `{"command":"echo x"}`,
		},
	})
	// Pre 按注册顺序，Post 逆序
	want := []string{"pre-a", "pre-b", "post-b", "post-a"}
	if len(order) != len(want) {
		t.Fatalf("顺序异常: %v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("顺序[%d] = %s, want %s (full %v)", i, order[i], want[i], order)
		}
	}
}

func TestBlockHook(t *testing.T) {
	r := NewRegistry()
	r.AddHook(BlockHook(map[string][]string{
		"exec_command": {"rm -rf"},
		"write_file":   {"/etc/"},
	}))
	// exec_command 命中
	res, _ := r.Execute(context.Background(), types.ToolCall{
		ID: "1",
		Function: types.FunctionCall{
			Name:      "exec_command",
			Arguments: `{"command":"rm -rf /tmp/x"}`,
		},
	})
	if res == nil || res.Error == "" {
		t.Errorf("rm -rf 应被拦截: %+v", res)
	}
	// write_file 命中
	res2, _ := r.Execute(context.Background(), types.ToolCall{
		ID: "2",
		Function: types.FunctionCall{
			Name:      "write_file",
			Arguments: `{"path":"/etc/passwd","content":"x"}`,
		},
	})
	if res2 == nil || res2.Error == "" {
		t.Errorf("/etc/ 写入应被拦截: %+v", res2)
	}
	// 未命中正常
	res3, _ := r.Execute(context.Background(), types.ToolCall{
		ID: "3",
		Function: types.FunctionCall{
			Name:      "exec_command",
			Arguments: `{"command":"echo ok"}`,
		},
	})
	if res3 == nil || res3.Error != "" {
		t.Errorf("正常命令不应拦截: %+v", res3)
	}
}

func TestLoggingHook(t *testing.T) {
	var logs []string
	r := NewRegistry()
	r.AddHook(LoggingHook(func(format string, args ...any) {
		logs = append(logs, format)
	}))
	r.Execute(context.Background(), types.ToolCall{
		ID: "1",
		Function: types.FunctionCall{
			Name:      "exec_command",
			Arguments: `{"command":"echo x"}`,
		},
	})
	if len(logs) == 0 {
		t.Error("LoggingHook 应产生日志")
	}
}
