package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestParse_ValidExpressions(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"* * * * *", true},
		{"*/5 * * * *", true},
		{"0 9 * * 1-5", true},
		{"30 8,18 * * *", true},
		{"0 0 1 * *", true},
		{"15 14 1 * *", true},
		{"0 22 * * 1-5", true},
		{"5 4 * * sun", false}, // 不支持英文名
		{"", false},
		{"* * * *", false},      // 4 字段
		{"* * * * * *", false},  // 6 字段
		{"a * * * *", false},    // 非法值
		{"60 * * * *", false},   // 分越界
		{"* 24 * * *", false},   // 时越界
		{"* * 32 * *", false},   // 日越界
		{"* * * 13 *", false},   // 月越界
		{"* * * * 8", false},    // 周越界（0-7）
		{"*/0 * * * *", false},  // 步长 0
		{"1-5/2 * * * *", true}, // 范围步长
		{"1,2,3 * * * *", true}, // 列表
		{"* * * * 7", true},     // 7=周日
	}
	for _, c := range cases {
		_, err := Parse(c.expr)
		if (err == nil) != c.want {
			t.Errorf("Parse(%q) err=%v, want ok=%v", c.expr, err, c.want)
		}
	}
}

func TestMatch_Basic(t *testing.T) {
	s, err := Parse("30 9 * * *") // 每天 09:30
	if err != nil {
		t.Fatal(err)
	}
	if !s.Match(time.Date(2026, 8, 7, 9, 30, 0, 0, time.Local)) {
		t.Error("09:30 应命中")
	}
	if s.Match(time.Date(2026, 8, 7, 9, 31, 0, 0, time.Local)) {
		t.Error("09:31 不应命中")
	}
}

func TestMatch_Weekday(t *testing.T) {
	s, _ := Parse("0 9 * * 1-5")                                 // 工作日 09:00
	if !s.Match(time.Date(2026, 8, 7, 9, 0, 0, 0, time.Local)) { // 周五
		t.Error("周五应命中")
	}
	if s.Match(time.Date(2026, 8, 8, 9, 0, 0, 0, time.Local)) { // 周六
		t.Error("周六不应命中")
	}
}

func TestMatch_DayOrWeek(t *testing.T) {
	// 日/周任一命中（标准 cron 语义）
	s, _ := Parse("0 0 1 * 0") // 每月 1 号 或 每周日
	if !s.Match(time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local)) {
		t.Error("8月1日应命中（日匹配）")
	}
	if !s.Match(time.Date(2026, 8, 2, 0, 0, 0, 0, time.Local)) { // 周日
		t.Error("周日应命中（周匹配）")
	}
	if s.Match(time.Date(2026, 8, 3, 0, 0, 0, 0, time.Local)) { // 周一
		t.Error("普通周一不应命中")
	}
}

func TestNext_Sequential(t *testing.T) {
	s, _ := Parse("*/10 * * * *") // 每 10 分钟
	base := time.Date(2026, 8, 7, 10, 3, 0, 0, time.Local)
	n1 := s.Next(base)
	if n1 != time.Date(2026, 8, 7, 10, 10, 0, 0, time.Local) {
		t.Errorf("下次应 10:10, got %v", n1)
	}
	n2 := s.Next(n1)
	if n2 != time.Date(2026, 8, 7, 10, 20, 0, 0, time.Local) {
		t.Errorf("再下次应 10:20, got %v", n2)
	}
}

func TestNext_Daily(t *testing.T) {
	s, _ := Parse("0 9 * * *")
	base := time.Date(2026, 8, 7, 10, 0, 0, 0, time.Local)
	n := s.Next(base)
	if n != time.Date(2026, 8, 8, 9, 0, 0, 0, time.Local) {
		t.Errorf("次日 09:00, got %v", n)
	}
}

func TestScheduler_AddInvalidExpr(t *testing.T) {
	s := New(10 * time.Millisecond)
	err := s.AddJob("bad", "99 * * * *", func(ctx context.Context) error { return nil })
	if err == nil {
		t.Fatal("非法表达式应报错")
	}
	if _, ok := err.(*JobError); !ok {
		t.Errorf("应为 JobError, got %T", err)
	}
}

func TestScheduler_DuplicateName(t *testing.T) {
	s := New(10 * time.Millisecond)
	fn := func(ctx context.Context) error { return nil }
	if err := s.AddJob("a", "* * * * *", fn); err != nil {
		t.Fatal(err)
	}
	if err := s.AddJob("a", "*/5 * * * *", fn); err == nil {
		t.Fatal("重名应报错")
	}
}

func TestScheduler_RunsJob(t *testing.T) {
	s := New(10 * time.Millisecond)
	var n int32
	if err := s.AddJob("every-min", "* * * * *", func(ctx context.Context) error {
		atomic.AddInt32(&n, 1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !s.Trigger("every-min") {
		t.Fatal("Trigger 应成功")
	}
	if s.Trigger("nope") {
		t.Fatal("不存在的任务 Trigger 应返回 false")
	}
	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&n) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if atomic.LoadInt32(&n) == 0 {
		t.Fatal("任务应被执行")
	}
	st := s.Status()
	if len(st) != 1 || st[0].Name != "every-min" || st[0].Runs < 1 {
		t.Errorf("状态异常: %+v", st)
	}
}

func TestScheduler_ErrorRecorded(t *testing.T) {
	s := New(10 * time.Millisecond)
	var errs atomic.Int32
	s.SetErrorHandler(func(name string, err error) { errs.Add(1) })
	if err := s.AddJob("fail", "* * * * *", func(ctx context.Context) error {
		return &JobError{Name: "fail", Msg: "boom"}
	}); err != nil {
		t.Fatal(err)
	}
	if !s.Trigger("fail") {
		t.Fatal("Trigger 应成功")
	}
	deadline := time.Now().Add(3 * time.Second)
	for errs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if errs.Load() == 0 {
		t.Fatal("错误回调应被触发")
	}
	st := s.Status()
	if len(st) != 1 || st[0].LastErr == "" {
		t.Errorf("LastErr 应记录: %+v", st)
	}
}

func TestScheduler_RemoveJob(t *testing.T) {
	s := New(10 * time.Millisecond)
	fn := func(ctx context.Context) error { return nil }
	_ = s.AddJob("a", "* * * * *", fn)
	if !s.RemoveJob("a") {
		t.Error("移除应成功")
	}
	if s.RemoveJob("a") {
		t.Error("重复移除应失败")
	}
}

func TestSortedValues(t *testing.T) {
	s, _ := Parse("1,5,10 8-10 * * *")
	mins, hours, _, _, _ := s.SortedValues()
	if len(mins) != 3 || mins[0] != 1 || mins[2] != 10 {
		t.Errorf("分字段解析错误: %v", mins)
	}
	if len(hours) != 3 || hours[0] != 8 || hours[2] != 10 {
		t.Errorf("时字段解析错误: %v", hours)
	}
}
