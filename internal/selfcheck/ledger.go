package selfcheck

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefectSignature 反复出现的缺陷签名（怀疑是"连续的"不是"一次性的"）。
type DefectSignature struct {
	Signature string `yaml:"signature"` // 如 "改完不跑测试"
	Count     int    `yaml:"count"`
	LastSeen  string `yaml:"last_seen"` // RFC3339 时间，可空
}

// Ledger 持久怀疑状态（quality_ledger.yaml，目标级随 checkpoint 持久化）。
// 累计每次裁决的缺陷签名 → 质检员下次优先核查同类问题。
type Ledger struct {
	DefectHistory []DefectSignature `yaml:"defect_history"`
}

// NewLedger 空账本。
func NewLedger() *Ledger { return &Ledger{} }

// Record 记录一次缺陷签名（已存在则计数 +1 并刷新时间；不存在则新增）。
func (l *Ledger) Record(defectSig string) {
	sig := strings.TrimSpace(defectSig)
	if sig == "" {
		return
	}
	for i := range l.DefectHistory {
		if l.DefectHistory[i].Signature == sig {
			l.DefectHistory[i].Count++
			l.DefectHistory[i].LastSeen = time.Now().Format(time.RFC3339)
			return
		}
	}
	l.DefectHistory = append(l.DefectHistory, DefectSignature{
		Signature: sig,
		Count:     1,
		LastSeen:  time.Now().Format(time.RFC3339),
	})
}

// TopSuspects 出现频次最高的 n 个缺陷签名（降序；频次相同按签名排序保证确定性）。
func (l *Ledger) TopSuspects(n int) []DefectSignature {
	out := make([]DefectSignature, len(l.DefectHistory))
	copy(out, l.DefectHistory)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Signature < out[j].Signature
	})
	if n >= 0 && n < len(out) {
		out = out[:n]
	}
	return out
}

// Count 某签名的累计次数。
func (l *Ledger) Count(sig string) int {
	for _, d := range l.DefectHistory {
		if d.Signature == sig {
			return d.Count
		}
	}
	return 0
}

// MarshalYAML 序列化（quality_ledger.yaml 落盘）。
func (l *Ledger) MarshalYAML() ([]byte, error) { return yaml.Marshal(l) }

// UnmarshalYAML 反序列化。
func (l *Ledger) UnmarshalYAML(data []byte) error { return yaml.Unmarshal(data, l) }

// String 摘要（GUI 只读面板展示）。
func (l *Ledger) String() string {
	if len(l.DefectHistory) == 0 {
		return "质量账本为空（尚无缺陷记录）"
	}
	top := l.TopSuspects(3)
	parts := make([]string, 0, len(top))
	for _, t := range top {
		parts = append(parts, fmt.Sprintf("%s×%d", t.Signature, t.Count))
	}
	return "历史高频缺陷：" + strings.Join(parts, "；")
}
