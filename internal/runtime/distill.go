// Package runtime —— 员工本体能力自动蒸馏（M3+ 自动化闭环）。
//
// 背景：员工工作区 ONTOLOGY.md 是"本体能力蒸馏载体"，但此前蒸馏工作
// 完全依赖用户手工沉淀（模板里全是"示例，可编辑"占位）——用户质问
// "你截断了文档，那蒸馏的工作谁去做？本来不是你做吗"点破了缺口：
// 蒸馏应该是 Agent 的自动职责，而非用户的功课。
//
// 本文件实现自动蒸馏闭环：子任务（SpawnTask）完成并命中员工时，用模型
// 把本次任务产出提炼成"经验条目"（金规/踩坑/风格），去重追加进该员工的
// workspace/ONTOLOGY.md（「## 经验蒸馏（自动沉淀）」段落）。下一次指派
// 该员工时，这些经验随本体能力说明书注入上下文——员工越干越熟，
// "本体能力蒸馏"真正由 Agent 自动完成。
//
// 约束：
//   - 零配置兜底：无员工档案/未命中员工 → 不蒸馏（SpawnTask 旧路径零变化）；
//   - 非阻塞：蒸馏在后台 goroutine 执行，绝不影响子任务返回；
//   - 幂等：内容哈希去重，同一经验不重复追加，文档不无限膨胀；
//   - 容量：单次蒸馏 ≤ 1KB，注入侧 16KB 上限（P1-1）继续兜底；
//   - 失败降级：模型调用/读写失败只记日志，绝不打扰用户、绝不阻断主流程。
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/contextloom/contextloom-agent/internal/modelgateway"
	"github.com/contextloom/contextloom-agent/internal/orchestrator"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── 自动蒸馏常量 ──

// distillSectionTitle 本体能力说明书中自动蒸馏段落的标题。
// 该段落由 Agent 自动维护；用户手工内容（其余章节）绝不被触碰。
const distillSectionTitle = "## 经验蒸馏（自动沉淀）"

// distillMaxLines 单次蒸馏最多沉淀的经验条目数（防单次爆量）。
const distillMaxLines = 4

// distillMaxItemBytes 单条经验条目的最大字节数（防单条过长撑爆文档）。
const distillMaxItemBytes = 300

// distillMaxDocGrowth 单次蒸馏允许的最大文档增长字节数（防无限膨胀）。
const distillMaxDocGrowth = 1024

// distillPromptTmpl 蒸馏提示词：要求模型把一次任务产出提炼成
// 可复用的经验条目（金规/踩坑/风格），供该员工后续任务直接复用。
// 输出格式为每行一条 "- 经验：…"，便于解析与去重。
const distillPromptTmpl = `你是员工「%s」的本体蒸馏器。请把下面这次任务执行过程与产出，提炼成 %d 条以内、可复用的经验条目（金规/踩坑/风格/技巧），供该员工后续执行同类任务时直接复用。
要求：
- 每条以 "- 经验：" 开头，单条不超过 %d 字；
- 只写"下次怎么做更好"，不写任务结果总结；
- 宁缺毋滥：没有值得沉淀的经验就输出 "无"。

任务描述：%s

本次产出（截断）：
%s`

// ── 自动蒸馏实现 ──

// distillWriteMu 串行化同进程内的蒸馏落盘（读-改-写原子性）：
// 同一员工的两个子任务并发完成时，防止后写覆盖前写丢失一批经验。
// 进程级单锁足够（蒸馏是低频后台操作，不追求并发吞吐）。
var distillWriteMu sync.Mutex

// distillEmployee 把一次子任务产出蒸馏为经验条目，追加进员工工作区
// ONTOLOGY.md 的自动蒸馏段落。在后台 goroutine 调用（SpawnTask 尾部触发，
// 不阻塞主流程）。员工缺失/无工作区/蒸馏失败 → 静默降级（仅日志）。
func (rt *Runtime) distillEmployee(emp *orchestrator.Employee, taskDesc, output string) {
	if emp == nil || rt.Config == nil {
		return
	}
	if !employeeSafeNameRe.MatchString(emp.Name) {
		return // 非法员工名：拒绝写任何路径（与注入侧同一白名单）
	}
	dir := filepath.Join(rt.Config.Project.Root, ".contextloom", employeesDir, emp.Name)
	// 工作区路径解析与注入侧一致（OntologyPath 相对员工目录）
	rel := emp.OntologyPath()
	p := rel
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, rel)
	}
	clean := filepath.Clean(p)
	dirClean := filepath.Clean(dir)
	if clean != dirClean && !strings.HasPrefix(clean, dirClean+string(filepath.Separator)) {
		return // 越界路径：拒绝写（P2-1 同源防护）
	}
	// 文档不存在（用户删了工作区）→ 不重建，静默跳过（零配置兜底）
	existing, err := os.ReadFile(clean)
	if err != nil {
		return
	}
	doc := string(existing)

	// 调用模型蒸馏（带超时，失败不阻断）
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	items := rt.distillWithModel(ctx, emp, taskDesc, output)
	if len(items) == 0 {
		log.Printf("[distill] 员工 %q 蒸馏无产出（模型返回空/失败），跳过本次沉淀", emp.Name)
		return
	}

	// 去重 + 合并进文档（读-改-写在锁内串行，防并发丢批次）
	distillWriteMu.Lock()
	defer distillWriteMu.Unlock()
	// 锁内重读：并发蒸馏可能已由其他 goroutine 写入，重新加载最新文档
	if data, err := os.ReadFile(clean); err == nil {
		doc = string(data)
	}
	updated := appendDistilledItems(doc, items)
	if updated == doc {
		log.Printf("[distill] 员工 %q 蒸馏结果与已有经验重复，跳过写入", emp.Name)
		return
	}
	if err := os.WriteFile(clean, []byte(updated), 0o644); err != nil {
		log.Printf("[distill] 员工 %q 经验落盘失败: %v", emp.Name, err)
		return
	}
	log.Printf("[distill] 员工 %q 自动沉淀 %d 条经验 → %s", emp.Name, len(items), clean)
}

// distillWithModel 用模型把任务产出蒸馏成经验条目列表（解析后的纯文本行）。
// 模型缺失/调用失败/解析不到有效条目 → 返回 nil（调用方静默降级）。
func (rt *Runtime) distillWithModel(ctx context.Context, emp *orchestrator.Employee, taskDesc, output string) []string {
	if rt.Gateway == nil && rt.Backend == nil {
		return nil
	}
	// 产出截断保护：蒸馏输入过大时只取头部（经验提炼只需任务意图 + 关键做法）
	outputSnippet := output
	const maxSnippet = 4000
	if len(outputSnippet) > maxSnippet {
		outputSnippet = outputSnippet[:maxSnippet]
	}
	prompt := fmt.Sprintf(distillPromptTmpl, emp.Name, distillMaxLines, distillMaxItemBytes, taskDesc, outputSnippet)
	req := modelgateway.Req{Task: "distill:" + emp.Name}
	backend := rt.selectBackend(ctx, req)
	if backend == nil {
		return nil
	}
	msg, _, err := backend.Chat(ctx, []types.Message{{Role: "user", Content: prompt}}, nil)
	if err != nil {
		log.Printf("[distill] 员工 %q 蒸馏模型调用失败: %v", emp.Name, err)
		return nil
	}
	return parseDistilledItems(msg)
}

// parseDistilledItems 解析模型蒸馏输出中的经验条目行。
// 识别以 "- 经验："（容错 "经验："）开头的行；条目去空格、去重、限长、
// 限条数。解析不到任何条目 → nil（含模型回答"无"的情况）。
func parseDistilledItems(msg string) []string {
	var items []string
	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimSpace(line)
		var body string
		switch {
		case strings.HasPrefix(line, "- 经验："):
			body = strings.TrimPrefix(line, "- 经验：")
		case strings.HasPrefix(line, "经验："):
			body = strings.TrimPrefix(line, "经验：")
		default:
			continue
		}
		body = strings.TrimSpace(body)
		if body == "" || body == "无" {
			continue
		}
		if len(body) > distillMaxItemBytes {
			body = body[:distillMaxItemBytes]
		}
		// 逐条去重（同一模型输出内）
		dup := false
		for _, it := range items {
			if it == body {
				dup = true
				break
			}
		}
		if !dup {
			items = append(items, body)
		}
		if len(items) >= distillMaxLines {
			break
		}
	}
	return items
}

// appendDistilledItems 把经验条目去重合并进本体能力说明书文本。
//
// 布局规则：
//   - 已有「## 经验蒸馏（自动沉淀）」段落 → 追加到段落内，逐条按内容哈希
//     与文档内全部条目去重（幂等：同一条经验不重复出现）；
//   - 无该段落 → 在文档尾部追加段落头 + 条目；
//   - 段落尾部无空行时补一个空行（保持 Markdown 可读性）；
//   - 单次增长超 distillMaxDocGrowth 时整体拒绝（防爆量）。
//
// 返回合并后的全文；无可追加（全部重复/超限）时返回原文本。
func appendDistilledItems(doc string, items []string) string {
	if len(items) == 0 {
		return doc
	}
	// 已存在条目哈希集合（防跨次重复）
	existingHashes := map[string]bool{}
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- 经验：") {
			body := strings.TrimPrefix(line, "- 经验：")
			existingHashes[hashBody(body)] = true
		}
	}
	// 新条目：跳过已存在的
	var fresh []string
	for _, it := range items {
		if !existingHashes[hashBody(it)] {
			fresh = append(fresh, it)
			existingHashes[hashBody(it)] = true
		}
	}
	if len(fresh) == 0 {
		return doc // 全部重复：原样返回
	}
	// 增长预算检查
	growth := 0
	for _, it := range fresh {
		growth += len("- 经验："+it+"\n") + 1
	}
	if growth > distillMaxDocGrowth {
		return doc // 超限：整体拒绝（宁可少沉淀，不撑爆文档）
	}
	// 定位段落
	idx := strings.Index(doc, distillSectionTitle)
	// 统一以 "- 经验：" 前缀写回（fresh 存的是纯 body，写回补前缀，
	// 保证下次去重扫描（按前缀识别）能命中，实现跨次幂等）
	lines := make([]string, 0, len(fresh))
	for _, it := range fresh {
		lines = append(lines, "- 经验："+it)
	}
	if idx < 0 {
		// 无段落：文档尾部追加（先补空行分隔）
		if !strings.HasSuffix(doc, "\n") {
			doc += "\n"
		}
		if !strings.HasSuffix(doc, "\n\n") {
			doc += "\n"
		}
		doc += distillSectionTitle + "\n"
		doc += strings.Join(lines, "\n") + "\n"
		return doc
	}
	// 已有段落：在段落末尾（下一个 ## 或文档尾）之前插入
	segStart := idx + len(distillSectionTitle)
	// 找段落内已有条目结尾：段落后第一个 "\n\n" 或 "## " 或文档尾
	rest := doc[segStart:]
	cut := len(rest)
	if n := strings.Index(rest, "\n\n"); n >= 0 {
		cut = n
	}
	if n := strings.Index(rest, "\n## "); n >= 0 && n < cut {
		cut = n
	}
	insertAt := segStart + cut
	// 段落内已有条目 → 直接在其后追加；空段落 → 直接写条目
	sep := ""
	seg := doc[segStart:insertAt]
	if strings.TrimSpace(seg) != "" {
		if !strings.HasSuffix(doc[:insertAt], "\n") {
			sep = "\n"
		}
	}
	doc = doc[:insertAt] + sep + "\n" + strings.Join(lines, "\n") + doc[insertAt:]
	// 保证段落与后续内容之间有分隔（若后面紧跟 ##，补一个空行）
	if restAfter := doc[insertAt+len(sep)+1:]; strings.HasPrefix(restAfter, "## ") {
		doc = doc[:insertAt+len(sep)+1] + "\n" + doc[insertAt+len(sep)+1:]
	}
	return doc
}

// hashBody 计算一条经验条目的内容指纹（去重用）。
func hashBody(body string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(body)))
	return hex.EncodeToString(sum[:])
}
