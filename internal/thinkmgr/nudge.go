package thinkmgr

import (
	"fmt"
	"strings"
)

// ── 验证/续思协议（M4/M6）──

// DefaultVerifyTemplate 内置验证轮模板（CoVe 四要素；%d=轮次；中文、可配置）。
const DefaultVerifyTemplate = `【验证轮 · 第 %d 轮】请对上面的结论做一次独立复核（不要复述原答案）：
1) 列出结论依赖的关键假设（≤3 条），逐条判断证据是否充分；
2) 挑最不确定的 2 个点，各提 1 个验证问题并独立作答（不引用原答案措辞）；
3) 发现矛盾或缺口：先修正，再输出修正后的完整最终答案，并以 [THINK_NEEDS_FIX] 开头说明修正点；
4) 全部验证通过：直接输出最终答案，以 [THINK_VERIFIED] 结尾。`

// ContinueTemplates 续思模板（nudge 轮换）；BuildVerifyNudge 生成注入文本（tmpl 空=内置）。
var ContinueTemplates = []string{
	"【续思提示】请对上述结论做一次独立验证：列出关键假设并逐一核对，可调用工具查证；若结论不变，请说明验证过程后收尾。",
	"【续思提示】换一个角度复核：若上述结论有错，最可能错在哪里？请给出复核结论或补充边界条件。",
}

// BuildVerifyNudge 生成注入文本：tmpl 为空（或纯空白）时使用 DefaultVerifyTemplate，%d 填入轮次。
func BuildVerifyNudge(round int, tmpl string) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = DefaultVerifyTemplate
	}
	return fmt.Sprintf(tmpl, round)
}

// 判定标记（模型输出侧约定；executor 轮末 SettleRound 解析）。
const (
	MarkVerified = "[THINK_VERIFIED]"  // 验证通过（提前放行）
	MarkNeedsFix = "[THINK_NEEDS_FIX]" // 发现并修正（进入 refine）
)
