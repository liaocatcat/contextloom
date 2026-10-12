# minimal-anchor 插件

对标 dsh 极简模式（minimal）的 ContextLoom 示例插件——复刻 dsh 30 轮实验的核心发现：
**模型在训练分布中见得最多的是"极简 persona + 最少工具"，首轮提示词决定整场走向**。

## 机制：先窄后宽

| 轮次 | route 钩子 | message 钩子 | 工具范围 |
|---|---|---|---|
| 第 1 轮 | 极简 persona（训练分布） + `tool_scope: core` | 三锚完整引导（目标/收敛/反跑题） | 核心 14 工具 |
| 第 2 轮 | 空（释放，不稀释） | 收敛推进轻引导 | 全部工具 |
| 第 3 轮+ | 空 | 空 | 全部工具 |

## 行为带（`dev_anchor_mode` 工具切换，`.anchor_state` 持久）

- **auto**（默认）：首轮极简锚定 → 第 2 轮释放
- **standard**：全部轮次不锚定（等同无插件）
- **full**：首轮即全能力（等同无插件）

## 安装

```bash
cp -r examples/plugins/minimal-anchor <项目>/.contextloom/plugins/minimal-anchor
```

自检：`bash verify.sh`（期望 34 PASS）

## 依赖

- bash + python3（仅 JSON 解析，无 python3 时 fallback grep）

## 验证

- `verify.sh` 34 断言：route 首轮锁定/释放、inject 三锚/收敛/空、工具往返、边界降级