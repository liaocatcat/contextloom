// Package runtime —— 场景触发器 → cron 通道接线（M4.4.3 triggers.cron）。
//
// 本文件把场景中心的 triggers.cron 声明接入现有 cron 通道：
//
//   - registerSceneCronJobs：扫描 .contextloom/scenes/（复用 scenes.LoadAll），
//     凡 Trigger.Cron 非空的场景注册为 "scene:<name>" 定时任务，
//     到点以 chat 模式注入场景触发词（第一个非空关键词），命中意图后
//     走场景一键执行（launchScene），与用户在聊天框输入关键词完全等价；
//   - 幂等：job 名冲突（config jobs 重名 / 重复加载）忽略，不重复注册；
//   - 零配置兜底：scenesDir 不存在 / sched 为 nil（cron 通道未启用）时
//     直接返回 0——不阻塞启动、不改变旧行为；
//   - 独立函数注入 sched 参数：主路径（New）与单测均可直接调用。
//
// 全部实现只用标准库；无 cgo；与 scenes/scheduler 包单向依赖（不反向）。
package runtime

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/contextloom/contextloom-agent/internal/scenes"
	"github.com/contextloom/contextloom-agent/internal/scheduler"
	"github.com/contextloom/contextloom-agent/internal/webhook"
)

// sceneCronJobPrefix 场景触发器在 cron 调度器中的任务名前缀（scene:<name>），
// 与 config jobs 命名空间天然隔离（用户 config job 名撞前缀需显式写出）。
const sceneCronJobPrefix = "scene:"

// registerSceneCronJobs 把场景中心的 triggers.cron 接入 cron 调度器：
// 扫描 scenesDir（通常为 <root>/.contextloom/scenes/），凡声明 triggers.cron
// 的场景注册为 scene:<name> 定时任务（触发模式 chat，Meta=scene:<name>）。
// 返回成功注册的任务数。
//
// 兜底与幂等语义（全部不阻塞启动）：
//   - sched 为 nil（cron 通道未启用）→ 返回 0；
//   - scenesDir 不存在 → 静默返回 0（零配置 = 旧行为）；
//   - 目录存在但加载失败（坏 scene.yaml）→ 记日志返回 0；
//   - Triggers.Cron 为空 → 跳过该场景；
//   - 同 job 名已存在（AddJob 重名 / 重复调用）→ 忽略跳过；
//   - cron 表达式非法 → 记日志跳过该场景，继续注册其余场景。
func (rt *Runtime) registerSceneCronJobs(sched *scheduler.Scheduler, scenesDir string) int {
	if sched == nil {
		return 0
	}
	if _, err := os.Stat(scenesDir); err != nil {
		return 0 // 无场景目录：零配置兜底，静默跳过
	}
	catalog, err := scenes.LoadAll(scenesDir)
	if err != nil {
		log.Printf("[cron] 场景目录 %s 加载失败（跳过场景触发器）: %v", scenesDir, err)
		return 0
	}
	added := 0
	for _, sc := range catalog {
		expr := strings.TrimSpace(sc.Triggers.Cron)
		if expr == "" {
			continue
		}
		jobName := sceneCronJobPrefix + sc.Name
		if _, dup := sched.Jobs()[jobName]; dup {
			continue // 幂等：同名任务已存在（config jobs 或重复调用）
		}
		sc := sc // 显式捕获（闭包持有场景名与触发词）
		text := sceneCronPrompt(sc)
		if err := sched.AddJob(jobName, expr, func(ctx context.Context) error {
			_, err := rt.RunChannelMessage(ctx, webhook.Message{Text: text, Mode: "chat", Meta: sceneCronJobPrefix + sc.Name})
			return err
		}); err != nil {
			log.Printf("[cron] 场景 %s 触发器注册失败: %v", sc.Name, err)
			continue
		}
		added++
	}
	return added
}

// sceneCronPrompt 场景定时触发的输入文本：优先第一个非空关键词（意图规则层
// 命中词，与用户在聊天框输入同态），其次展示名（简短名），最后场景名。
func sceneCronPrompt(sc scenes.Scene) string {
	for _, kw := range sc.Keywords {
		if kw = strings.TrimSpace(kw); kw != "" {
			return kw
		}
	}
	if d := strings.TrimSpace(sc.Display); d != "" {
		return d
	}
	return sc.Name
}
