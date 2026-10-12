package selfcheck

import (
	"fmt"
	"strings"
)

// ArtifactType 产物类型（测试 TEE 验证矩阵的行）。
type ArtifactType string

const (
	// ArtifactGoCode Go 代码改动：必须 编译 + 测试 双通过。
	ArtifactGoCode ArtifactType = "go_code"
	// ArtifactNonGoCode 非 Go 代码改动：run_tests 代理（测试通过即可）。
	ArtifactNonGoCode ArtifactType = "non_go_code"
	// ArtifactDoc 非代码产物（文档/PRD/周报/分析）：TEE 不适用，改走逻辑自洽闭环核查。
	ArtifactDoc ArtifactType = "doc"
)

// ParseArtifactType 解析产物类型字符串（大小写不敏感，非法返回空）。
func ParseArtifactType(s string) ArtifactType {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "go_code", "gocode", "go":
		return ArtifactGoCode
	case "non_go_code", "nongocode", "py", "js", "ts":
		return ArtifactNonGoCode
	case "doc", "document", "md":
		return ArtifactDoc
	default:
		return ""
	}
}

// TestTEEResult 测试 TEE 一次执行的结果（发证依据）。
type TestTEEResult struct {
	ArtifactType ArtifactType `yaml:"artifact_type"`
	Compiled     bool         `yaml:"compiled"`    // 编译门禁通过（go build ./...）
	TestPassed   bool         `yaml:"test_passed"` // 测试执行通过（go test / run_tests 代理）
	TestCount    int          `yaml:"test_count"`  // 执行的用例数
	Output       string       `yaml:"output"`      // 失败输出（成功可为空）
	SHA          string       `yaml:"sha"`         // 产物哈希（证据链）
}

// TEEVerdict 测试 TEE 裁决。
type TEEVerdict int

const (
	// TEEFailed 未通过（自报不信，证据不足即失败）。
	TEEFailed TEEVerdict = iota
	// TEEPassed 通过（端到端验证完成，可发证）。
	TEEPassed
	// TEENeedsLogicCheck 不适用 TEE（文档类）：改走逻辑自洽闭环核查。
	TEENeedsLogicCheck
)

// String 裁决的可读文案。
func (v TEEVerdict) String() string {
	switch v {
	case TEEPassed:
		return "passed"
	case TEENeedsLogicCheck:
		return "needs_logic_check"
	default:
		return "failed"
	}
}

// Verify 按产物类型裁决测试 TEE 结果（§4.6.2 验证矩阵）：
//   - GoCode：Compiled && TestPassed 才通过；
//   - NonGoCode：TestPassed 通过（run_tests 代理语义）；
//   - Doc：TEE 不适用 → TEENeedsLogicCheck；
//   - 未知/空类型：保守失败。
func Verify(r TestTEEResult) TEEVerdict {
	switch r.ArtifactType {
	case ArtifactGoCode:
		if r.Compiled && r.TestPassed {
			return TEEPassed
		}
		return TEEFailed
	case ArtifactNonGoCode:
		if r.TestPassed {
			return TEEPassed
		}
		return TEEFailed
	case ArtifactDoc:
		return TEENeedsLogicCheck
	default:
		return TEEFailed
	}
}

// Certificate 测试 TEE 发证记录（quality_ledger.yaml 内的一条证据）。
type Certificate struct {
	SHA       string     `yaml:"sha"`
	Artifact  string     `yaml:"artifact"` // 产物类型
	TestCount int        `yaml:"test_count"`
	Verdict   TEEVerdict `yaml:"verdict"`
	Output    string     `yaml:"output,omitempty"`
}

// NewCertificate 由 TEE 结果生成证书（Verdict != passed 时不发证，返回 false）。
func NewCertificate(r TestTEEResult) (Certificate, bool) {
	v := Verify(r)
	if v != TEEPassed {
		return Certificate{SHA: r.SHA, Artifact: string(r.ArtifactType), Verdict: v, Output: r.Output}, false
	}
	return Certificate{SHA: r.SHA, Artifact: string(r.ArtifactType), TestCount: r.TestCount, Verdict: v}, true
}

// String 结果摘要（注入缺陷清单提示词用）。
func (r TestTEEResult) String() string {
	v := Verify(r)
	switch v {
	case TEEPassed:
		return fmt.Sprintf("测试TEE 通过（%s，%d 用例）", r.ArtifactType, r.TestCount)
	case TEENeedsLogicCheck:
		return fmt.Sprintf("测试TEE 不适用（%s 文档类产物，改走逻辑自洽核查）", r.ArtifactType)
	default:
		return fmt.Sprintf("测试TEE 未通过（%s，compiled=%v passed=%v）：%s",
			r.ArtifactType, r.Compiled, r.TestPassed, r.Output)
	}
}
