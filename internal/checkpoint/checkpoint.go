// Package checkpoint 提供多 Agent 运行检查点持久化（对标 LangGraph checkpointer
// 的零依赖落地）：
//
//   - 每 run 一个 JSONL 文件（run-<runID>.jsonl），每轮执行追加一行完整快照；
//   - LoadLatest 读取最后一行（最新状态），List 汇总全部 run 元信息；
//   - Prune 按更新时间保留最新 max 个 run，防止无限膨胀；
//   - 纯 Go 零依赖，全平台可交叉编译；Save/Prune 失败返回错误但语义非致命
//     （检查点不应阻断 agent 主流程）。
//
// 用法：executor.Agent 每轮消息更新后调用 CheckpointSink 落盘快照；
// 任务恢复（如 goal 自动续跑、异常重启）时 LoadLatest 恢复消息上下文。
package checkpoint

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// 单 run 检查点文件大小上限：超过后 Save 触发"滚动重写"（只保留最近
// keepCheckpointLines 条快照），防止无界追加产生 GB 级巨文件
// （历史缺陷：run-main.jsonl 膨胀到 932MB → 启动时全量读取 → 切换项目
// 超时 12s+，见 loadLastLocked 注释）。
//
// 减容参数（缓存膨胀治理）：恢复只需最新一条快照，历史快照仅用于审计——
// 因此上限从 64MB 收到 16MB、保留条数从 100 收到 30，单行消息体另由
// 调用方在 Save 前裁剪（见 runtime.attachCheckpointSink），三层叠加把
// 单 run 文件体积压到 MB 级。
const (
	maxCheckpointFileSize = 16 * 1024 * 1024 // 16MB
	keepCheckpointLines   = 30               // 滚动重写保留最近条数
)

// Checkpoint 单轮运行快照。
type Checkpoint struct {
	ID        string    `json:"id"`                   // 快照 ID（自动生成）
	SessionID string    `json:"session_id,omitempty"` // 会话 ID
	Mode      string    `json:"mode,omitempty"`       // chat / task / goal
	RunID     string    `json:"run_id"`               // 运行 ID（每 run 一个文件）
	Round     int       `json:"round"`                // 已完成的轮次
	Messages  []byte    `json:"messages,omitempty"`   // 对话消息（JSON 序列化）
	Summary   string    `json:"summary,omitempty"`    // 进度摘要
	Done      bool      `json:"done"`                 // 是否已结束
	Result    string    `json:"result,omitempty"`     // 最终结果（done=true 时）
	CreatedAt time.Time `json:"created_at"`           // 首次快照时间
	UpdatedAt time.Time `json:"updated_at"`           // 最近快照时间
}

// Meta List 返回的 run 元信息。
type Meta struct {
	RunID     string
	Round     int
	UpdatedAt time.Time
	Done      bool
	Mode      string
	SessionID string
}

// Store 检查点存储（目录 + JSONL 文件）。
type Store struct {
	dir string
	mu  sync.Mutex
}

// New 创建 Store，目录不存在则创建。
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建检查点目录失败: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir 返回存储目录。
func (s *Store) Dir() string { return s.dir }

// fileFor 返回 runID 对应的 JSONL 文件路径。
func (s *Store) fileFor(runID string) string {
	return filepath.Join(s.dir, "run-"+runID+".jsonl")
}

// Save 追加一条快照。ID 为空自动生成；CreatedAt 为零时取文件首行时间
// （已有快照则保持首次时间，否则为当前时间）；UpdatedAt 恒为当前时间。
func (s *Store) Save(cp Checkpoint) error {
	if cp.RunID == "" {
		return errors.New("checkpoint: run_id 不能为空")
	}
	if cp.ID == "" {
		cp.ID = uuid.NewString()
	}
	now := time.Now()
	if cp.CreatedAt.IsZero() {
		first, err := s.loadFirstLocked(s.fileFor(cp.RunID))
		if err == nil && first != nil {
			cp.CreatedAt = first.CreatedAt
		} else {
			cp.CreatedAt = now
		}
	}
	cp.UpdatedAt = now

	data, err := json.Marshal(cp)
	if err != nil {
		return fmt.Errorf("checkpoint: 序列化失败: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.fileFor(cp.RunID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("checkpoint: 打开文件失败: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("checkpoint: 写入失败: %w", err)
	}
	// 文件大小限流：超过上限触发滚动重写（只保留最近 keepCheckpointLines 条）。
	// 防止无界追加产生 GB 级巨文件（历史缺陷：run-main.jsonl 932MB → 启动
	// 全量读取 12s+ → GUI 切换项目超时）。重写失败仅记录不阻断写入语义
	// （检查点非致命）。
	if fi, err := f.Stat(); err == nil && fi.Size() > maxCheckpointFileSize {
		_ = s.rolloverLocked(cp.RunID)
	}
	return nil
}

// rolloverLocked 把 run 文件重写为只保留最近 keepCheckpointLines 条快照
// （原子替换：临时文件 + Rename）。调用方须持有 s.mu。
func (s *Store) rolloverLocked(runID string) error {
	path := s.fileFor(runID)
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return err
	}
	// 从尾部读取窗口（上限 maxCheckpointFileSize，与限流阈值一致），
	// 从后向前解析最多 keepCheckpointLines 条有效快照。
	readSize := fi.Size()
	if readSize > maxCheckpointFileSize {
		readSize = maxCheckpointFileSize
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, readSize)
	if _, err := f.ReadAt(buf, fi.Size()-readSize); err != nil && err != io.EOF {
		return err
	}
	var lines []string
	for i := len(buf) - 1; i >= 0; {
		// 向前找上一个 '\n'（跳过尾部可能的残缺行尾）
		nl := bytes.LastIndexByte(buf[:i+1], '\n')
		var seg string
		if nl < 0 {
			seg = strings.TrimSpace(string(buf[:i+1]))
			i = -1
		} else {
			seg = strings.TrimSpace(string(buf[nl+1 : i+1]))
			i = nl - 1
		}
		if seg == "" {
			if nl < 0 {
				break
			}
			continue
		}
		var cp Checkpoint
		if json.Unmarshal([]byte(seg), &cp) != nil {
			continue // 跳过损坏行
		}
		lines = append(lines, seg)
		if len(lines) >= keepCheckpointLines {
			break
		}
	}
	if len(lines) == 0 {
		return nil // 无有效行：不重写
	}
	// 逆序还原时间顺序，写临时文件原子替换
	var sb strings.Builder
	for i := len(lines) - 1; i >= 0; i-- {
		sb.WriteString(lines[i])
		sb.WriteByte('\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadLatest 读取 run 的最新快照（文件最后一行）。
// 文件不存在时返回包装 os.ErrNotExist 的错误（可用 errors.Is 判断）。
func (s *Store) LoadLatest(runID string) (*Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLastLocked(s.fileFor(runID))
}

func (s *Store) loadLastLocked(path string) (*Checkpoint, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("checkpoint: %w", os.ErrNotExist)
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() == 0 {
		return nil, fmt.Errorf("checkpoint: %w", os.ErrNotExist)
	}
	// 高效取最后一行：从文件尾部逐档扩大读取窗口（256KB → 512KB → …），
	// 解析窗口内最后一个换行之后的内容。
	// 历史缺陷：旧实现用 Scanner 全量扫描取最后一行——run-main.jsonl 膨胀到
	// 932MB 后每次 List/Prune/LoadLatest 都读完整文件，GUI 切换项目时
	// backend 启动卡 12s+（startBackend 15s 超时 → 切换失败）。seek 版只读
	// 尾部窗口（通常 256KB 即命中），超大单行场景逐档扩大直至覆盖。
	window := int64(256 * 1024)
	for window <= fi.Size() {
		start := fi.Size() - window
		buf := make([]byte, window)
		if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
			return nil, err
		}
		if cp := parseLastLine(buf); cp != nil {
			return cp, nil
		}
		if start == 0 {
			break
		}
		if window >= 64*1024*1024 {
			break // 异常大单行：兜底全量扫描
		}
		window *= 2
	}
	// 兜底：全量扫描（文件异常/全部损坏/单行超 64MB 时），语义与原实现一致
	return s.loadLastSlowLocked(path)
}

// parseLastLine 解析 buf 内最后一个换行之后的内容为 Checkpoint。
// 无换行/空行/解析失败返回 nil（调用方据此扩大窗口或兜底）。
func parseLastLine(buf []byte) *Checkpoint {
	idx := bytes.LastIndexByte(buf, '\n')
	if idx < 0 {
		return nil
	}
	line := strings.TrimSpace(string(buf[idx+1:]))
	if line == "" {
		return nil
	}
	var cp Checkpoint
	if err := json.Unmarshal([]byte(line), &cp); err != nil {
		return nil
	}
	return &cp
}

// loadLastSlowLocked 全量扫描取最后一行（仅异常兜底路径）。
func (s *Store) loadLastSlowLocked(path string) (*Checkpoint, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("checkpoint: %w", os.ErrNotExist)
		}
		return nil, err
	}
	defer f.Close()
	var last *Checkpoint
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var cp Checkpoint
		if err := json.Unmarshal([]byte(line), &cp); err != nil {
			continue // 跳过损坏行（不阻断）
		}
		last = &cp
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if last == nil {
		return nil, fmt.Errorf("checkpoint: %w", os.ErrNotExist)
	}
	return last, nil
}

func (s *Store) loadFirstLocked(path string) (*Checkpoint, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return nil, sc.Err()
	}
	var cp Checkpoint
	if err := json.Unmarshal(sc.Bytes(), &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

// List 返回全部 run 元信息（按 UpdatedAt 倒序，最近活跃在前）。
func (s *Store) List() ([]Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	matches, err := filepath.Glob(filepath.Join(s.dir, "run-*.jsonl"))
	if err != nil {
		return nil, err
	}
	var metas []Meta
	for _, m := range matches {
		last, err := s.loadLastLocked(m)
		if err != nil {
			continue
		}
		runID := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "run-"), ".jsonl")
		metas = append(metas, Meta{
			RunID:     runID,
			Round:     last.Round,
			UpdatedAt: last.UpdatedAt,
			Done:      last.Done,
			Mode:      last.Mode,
			SessionID: last.SessionID,
		})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].UpdatedAt.After(metas[j].UpdatedAt) })
	return metas, nil
}

// Prune 保留最新 max 个 run（按 UpdatedAt），删除其余 run 文件。
// max<=0 不操作。返回删除的文件数。
func (s *Store) Prune(max int) (int, error) {
	if max <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	metas, err := s.listLocked()
	if err != nil {
		return 0, err
	}
	if len(metas) <= max {
		return 0, nil
	}
	keep := make(map[string]bool, max)
	for i := 0; i < max && i < len(metas); i++ {
		keep[metas[i].RunID] = true
	}
	removed := 0
	matches, _ := filepath.Glob(filepath.Join(s.dir, "run-*.jsonl"))
	for _, m := range matches {
		runID := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "run-"), ".jsonl")
		if !keep[runID] {
			if err := os.Remove(m); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}

// PruneAged 按文件修改时间直接删除最旧的 run 文件，直至剩余 keep 个。
// 与 Prune 的区别：只看文件 mtime、不解析内容（listLocked 会对每个 run
// 读尾行），因此开销 O(文件数) 且与单文件大小无关——适合启动/退出/切换
// 项目等"不能卡顿"的路径做存量收敛（run 文件量级数百个以内时 <1ms）。
// keep<=0 不操作，返回删除的文件数。
func (s *Store) PruneAged(keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	matches, _ := filepath.Glob(filepath.Join(s.dir, "run-*.jsonl"))
	if len(matches) <= keep {
		return 0, nil
	}
	// 按 mtime 升序（最旧在前），仅保留最后 keep 个
	files := make([]string, 0, len(matches))
	for _, m := range matches {
		files = append(files, m)
	}
	sort.Slice(files, func(i, j int) bool {
		fi, ei := os.Stat(files[i])
		fj, ej := os.Stat(files[j])
		if ei != nil || ej != nil {
			return files[i] < files[j]
		}
		return fi.ModTime().Before(fj.ModTime())
	})
	removed := 0
	for i := 0; i < len(files)-keep; i++ {
		if err := os.Remove(files[i]); err == nil {
			removed++
		}
	}
	return removed, nil
}

func (s *Store) listLocked() ([]Meta, error) {
	matches, err := filepath.Glob(filepath.Join(s.dir, "run-*.jsonl"))
	if err != nil {
		return nil, err
	}
	var metas []Meta
	for _, m := range matches {
		last, err := s.loadLastLocked(m)
		if err != nil {
			continue
		}
		runID := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "run-"), ".jsonl")
		metas = append(metas, Meta{
			RunID:     runID,
			Round:     last.Round,
			UpdatedAt: last.UpdatedAt,
			Done:      last.Done,
			Mode:      last.Mode,
			SessionID: last.SessionID,
		})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].UpdatedAt.After(metas[j].UpdatedAt) })
	return metas, nil
}

// Delete 删除 run 的检查点文件。文件不存在视为成功（幂等）。
func (s *Store) Delete(runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.fileFor(runID))
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// Close 当前无资源需要释放（保留签名便于生命周期统一）。
func (s *Store) Close() error { return nil }
