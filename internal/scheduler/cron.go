// Package scheduler 提供轻量 cron 定时调度与 daemon 长跑能力
// （对标业界同类 Agent 的定时调度，零依赖纯 Go 实现）。
//
// 设计原则：
//   - 手写 5 字段 cron 表达式解析（分 时 日 月 周），零第三方依赖；
//   - 调度精度 30 秒内，任务执行放独立 goroutine（失败不阻塞调度循环）；
//   - 全部纯 Go，可交叉编译（无 CGO）。
package scheduler

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Schedule 解析后的 cron 调度计划。
type Schedule struct {
	expr    string
	minute  map[int]bool
	hour    map[int]bool
	day     map[int]bool
	month   map[int]bool
	weekday map[int]bool // 0=周日, 1-6, 7=周日（归一化为 0）
	hasDay  bool
	hasWeek bool
}

// Parse 解析 5 字段 cron 表达式：分 时 日 月 周。
// 支持：*（全部）、数字、范围 a-b、列表 a,b,c、步长 */n 与 a-b/n。
func Parse(expr string) (*Schedule, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron 表达式须为 5 字段（分 时 日 月 周），got %d 字段: %q", len(fields), expr)
	}
	s := &Schedule{expr: expr}
	var err error
	if s.minute, err = parseField(fields[0], 0, 59); err != nil {
		return nil, fmt.Errorf("分字段: %w", err)
	}
	if s.hour, err = parseField(fields[1], 0, 23); err != nil {
		return nil, fmt.Errorf("时字段: %w", err)
	}
	if s.day, err = parseField(fields[2], 1, 31); err != nil {
		return nil, fmt.Errorf("日字段: %w", err)
	}
	if s.month, err = parseField(fields[3], 1, 12); err != nil {
		return nil, fmt.Errorf("月字段: %w", err)
	}
	if s.weekday, err = parseField(fields[4], 0, 7); err != nil {
		return nil, fmt.Errorf("周字段: %w", err)
	}
	// 周 7 归一化为 0（都表示周日）
	if s.weekday[7] {
		s.weekday[0] = true
	}
	// 日/周同时受限时采用"或"语义（标准 cron：两字段任一匹配即触发）
	s.hasDay = fields[2] != "*"
	s.hasWeek = fields[4] != "*"
	return s, nil
}

// parseField 解析单个字段：* / 数字 / a-b / a,b,c / */n / a-b/n。
func parseField(field string, min, max int) (map[int]bool, error) {
	out := make(map[int]bool)
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("空字段项")
		}
		step := 1
		base := part
		if i := strings.Index(part, "/"); i >= 0 {
			base = part[:i]
			stepStr := part[i+1:]
			if stepStr == "" {
				return nil, fmt.Errorf("步长缺失: %q", part)
			}
			v, err := strconv.Atoi(stepStr)
			if err != nil || v <= 0 {
				return nil, fmt.Errorf("非法步长 %q", stepStr)
			}
			step = v
		}
		lo, hi := min, max
		if base != "*" {
			rng := strings.SplitN(base, "-", 2)
			a, err := strconv.Atoi(strings.TrimSpace(rng[0]))
			if err != nil {
				return nil, fmt.Errorf("非法值 %q", base)
			}
			if len(rng) == 2 {
				b, err := strconv.Atoi(strings.TrimSpace(rng[1]))
				if err != nil {
					return nil, fmt.Errorf("非法范围 %q", base)
				}
				lo, hi = a, b
			} else {
				lo, hi = a, a
			}
		}
		if lo < min || hi > max || lo > hi {
			return nil, fmt.Errorf("值域越界 %q（允许 %d~%d）", base, min, max)
		}
		for v := lo; v <= hi; v += step {
			out[v] = true
		}
	}
	return out, nil
}

// Match 判断给定时间是否命中计划（不含秒，按分钟精度匹配）。
func (s *Schedule) Match(t time.Time) bool {
	wd := int(t.Weekday()) // 0=周日
	if !s.minute[t.Minute()] || !s.hour[t.Hour()] || !s.month[int(t.Month())] {
		return false
	}
	dayMatch := s.day[t.Day()]
	weekMatch := s.weekday[wd]
	if s.hasDay && s.hasWeek {
		return dayMatch || weekMatch
	}
	if s.hasDay {
		return dayMatch
	}
	if s.hasWeek {
		return weekMatch
	}
	return true // 日、周均为 *：任意日
}

// Next 返回 after 之后（严格大于）的最近触发时间。
// 找不到（如 2 月 30 日）返回零值。
func (s *Schedule) Next(after time.Time) time.Time {
	// 向前探测最多 5 年，避免死循环
	limit := after.AddDate(5, 0, 0)
	t := after.Truncate(time.Minute).Add(time.Minute)
	for t.Before(limit) {
		if s.Match(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

// Expr 返回原始表达式。
func (s *Schedule) Expr() string { return s.expr }

// String 实现 fmt.Stringer。
func (s *Schedule) String() string { return s.expr }

// SortedValues 返回字段允许值的排序列表（用于调试与展示）。
func (s *Schedule) SortedValues() (minutes, hours, days, months, weekdays []int) {
	for _, m := range []struct {
		set map[int]bool
		dst *[]int
	}{{s.minute, &minutes}, {s.hour, &hours}, {s.day, &days}, {s.month, &months}, {s.weekday, &weekdays}} {
		for v := range m.set {
			*m.dst = append(*m.dst, v)
		}
		sort.Ints(*m.dst)
	}
	return
}
