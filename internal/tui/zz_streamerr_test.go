package tui

import (
	"errors"
	"testing"
)

// 验证 readNextStreamToken 的错误优先语义：errCh 有错误 + ch 同时关闭
// （agent return 前先发错误再 defer close(ch)）→ 必须返回 streamErrMsg，
// 不能因 select 竞态随机选中 ch 关闭而返回 streamDoneMsg（吞掉真实错误、
// 显示"工作完成"——对话中止的隐藏路径）。
func TestReadNextStreamToken_ErrorPriority(t *testing.T) {
	m := &model{}
	ch := make(chan string, 4)
	errCh := make(chan error, 1)
	m.streamCh = ch
	m.streamErrCh = errCh

	// 场景1：errCh 有错误 + ch 关闭（同时就绪，错误优先）
	close(ch)
	errCh <- errors.New("模型调用失败: API 400")
	msg := m.readNextStreamToken()()
	if _, ok := msg.(streamErrMsg); !ok {
		t.Fatalf("场景1: errCh 有错误时应返回 streamErrMsg，实际 %T (%v)", msg, msg)
	}

	// 场景2：errCh 关闭无错误 + ch 关闭 → 正常完成
	m2 := &model{}
	ch2 := make(chan string, 4)
	errCh2 := make(chan error, 1)
	m2.streamCh = ch2
	m2.streamErrCh = errCh2
	close(ch2)
	close(errCh2)
	msg = m2.readNextStreamToken()()
	if _, ok := msg.(streamDoneMsg); !ok {
		t.Fatalf("场景2: 无错误应返回 streamDoneMsg，实际 %T (%v)", msg, msg)
	}

	// 场景3：正常 token 流 → streamMsg
	m3 := &model{}
	ch3 := make(chan string, 4)
	errCh3 := make(chan error, 1)
	m3.streamCh = ch3
	m3.streamErrCh = errCh3
	ch3 <- "你好"
	msg = m3.readNextStreamToken()()
	if _, ok := msg.(streamMsg); !ok {
		t.Fatalf("场景3: 应返回 streamMsg，实际 %T (%v)", msg, msg)
	}
}

// 场景4（竞态回归）：errCh 先关闭（无错误）后 ch 关闭——阻塞在 ch 读取时
// errCh 的错误恰好到达 → 仍应捕获错误而非误判完成。
func TestReadNextStreamToken_ErrArrivesWhileReadingCh(t *testing.T) {
	m := &model{}
	ch := make(chan string, 4)
	errCh := make(chan error, 1)
	m.streamCh = ch
	m.streamErrCh = errCh

	done := make(chan struct{})
	go func() {
		defer close(done)
		errCh <- errors.New("连接中断")
		close(ch)
	}()

	// 直接调 readNextStreamToken 两次（第一次可能先到错误，也可能阻塞后 ch 关闭）
	msg := m.readNextStreamToken()()
	switch msg.(type) {
	case streamErrMsg:
		// ✅ 错误被捕获（无论竞态如何，错误优先逻辑保证）
	case streamMsg:
		// 先收到了 token（没有 token 可收，此分支不可能）
		t.Fatalf("不应收到 streamMsg: %v", msg)
	default:
		// streamDoneMsg 也不允许——错误被吞了
		t.Fatalf("错误被吞，返回 %T (%v)", msg, msg)
	}
	<-done
}
