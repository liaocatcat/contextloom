package scheduler

import (
	"context"
	"log"
	"sync"
	"time"
)

// JobFunc 任务执行函数。返回 error 时记录日志（不中断调度）。
type JobFunc func(ctx context.Context) error

// Job 一个定时任务。
type Job struct {
	Name     string
	Expr     string
	Schedule *Schedule
	Fn       JobFunc
	Next     time.Time
	LastRun  time.Time
	LastErr  string
	Runs     int
	mu       sync.Mutex
}

// Scheduler 轻量 cron 调度器。
type Scheduler struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	started bool
	stop    chan struct{}
	done    chan struct{}
	tick    time.Duration
	onErr   func(name string, err error) // 错误回调（可注入日志）
}

// New 创建调度器。tick 为检查间隔（默认 15s；调度精度 = tick + 执行时间）。
func New(tick time.Duration) *Scheduler {
	if tick <= 0 {
		tick = 15 * time.Second
	}
	return &Scheduler{
		jobs:  make(map[string]*Job),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
		tick:  tick,
		onErr: func(name string, err error) { log.Printf("[scheduler] 任务 %s 执行失败: %v", name, err) },
	}
}

// SetErrorHandler 设置任务错误回调。
func (s *Scheduler) SetErrorHandler(fn func(name string, err error)) {
	if fn != nil {
		s.onErr = fn
	}
}

// AddJob 注册任务。表达式非法时返回错误（不注册）。
func (s *Scheduler) AddJob(name, expr string, fn JobFunc) error {
	if fn == nil {
		return &JobError{Name: name, Msg: "任务函数为空"}
	}
	sc, err := Parse(expr)
	if err != nil {
		return &JobError{Name: name, Msg: "表达式非法: " + err.Error()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.jobs[name]; dup {
		return &JobError{Name: name, Msg: "任务名重复"}
	}
	s.jobs[name] = &Job{
		Name:     name,
		Expr:     expr,
		Schedule: sc,
		Fn:       fn,
		Next:     sc.Next(time.Now()),
	}
	return nil
}

// RemoveJob 移除任务。
func (s *Scheduler) RemoveJob(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.jobs[name]
	delete(s.jobs, name)
	return ok
}

// Jobs 返回任务快照（名称→下次触发时间）。
func (s *Scheduler) Jobs() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]time.Time, len(s.jobs))
	for n, j := range s.jobs {
		j.mu.Lock()
		out[n] = j.Next
		j.mu.Unlock()
	}
	return out
}

// JobStatus 单个任务状态（供展示）。
type JobStatus struct {
	Name    string    `json:"name"`
	Expr    string    `json:"expr"`
	Next    time.Time `json:"next"`
	LastRun time.Time `json:"last_run,omitempty"`
	LastErr string    `json:"last_err,omitempty"`
	Runs    int       `json:"runs"`
}

// Status 返回全部任务状态。
func (s *Scheduler) Status() []JobStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JobStatus, 0, len(s.jobs))
	for _, j := range s.jobs {
		j.mu.Lock()
		out = append(out, JobStatus{
			Name:    j.Name,
			Expr:    j.Expr,
			Next:    j.Next,
			LastRun: j.LastRun,
			LastErr: j.LastErr,
			Runs:    j.Runs,
		})
		j.mu.Unlock()
	}
	// 按下次触发时间排序
	for i := 1; i < len(out); i++ {
		for k := i; k > 0 && out[k].Next.Before(out[k-1].Next); k-- {
			out[k], out[k-1] = out[k-1], out[k]
		}
	}
	return out
}

// Start 启动调度循环（阻塞直到 ctx 取消或 Stop 调用）。
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	defer close(s.done)

	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case now := <-ticker.C:
			s.runDue(now)
		}
	}
}

// Stop 停止调度循环（Start 返回）。
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.started = false
	s.mu.Unlock()
	close(s.stop)
	<-s.done
	// 允许再次 Start
	s.mu.Lock()
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	s.mu.Unlock()
}

// Trigger 手动立即触发指定任务（供 CLI/daemon/测试使用）。
// 任务不存在返回 false。
func (s *Scheduler) Trigger(name string) bool {
	s.mu.Lock()
	j, ok := s.jobs[name]
	s.mu.Unlock()
	if !ok {
		return false
	}
	go s.exec(j)
	return true
}

// runDue 执行所有到期任务（每任务独立 goroutine，不阻塞）。
func (s *Scheduler) runDue(now time.Time) {
	s.mu.Lock()
	var due []*Job
	for _, j := range s.jobs {
		j.mu.Lock()
		if !j.Next.IsZero() && !now.Before(j.Next) {
			due = append(due, j)
		}
		j.mu.Unlock()
	}
	s.mu.Unlock()

	for _, j := range due {
		go s.exec(j)
	}
}

// exec 执行单个任务并更新状态。
func (s *Scheduler) exec(j *Job) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	start := time.Now()
	err := j.Fn(ctx)

	j.mu.Lock()
	defer j.mu.Unlock()
	j.LastRun = start
	j.Runs++
	if err != nil {
		j.LastErr = err.Error()
		s.onErr(j.Name, err)
	} else {
		j.LastErr = ""
	}
	// 计算下次触发（从最近一次触发点之后）
	base := start
	if n := j.Schedule.Next(base); !n.IsZero() {
		j.Next = n
	} else {
		j.Next = j.Schedule.Next(time.Now())
	}
}

// JobError 任务注册错误。
type JobError struct {
	Name string
	Msg  string
}

func (e *JobError) Error() string { return "调度任务 " + e.Name + ": " + e.Msg }
