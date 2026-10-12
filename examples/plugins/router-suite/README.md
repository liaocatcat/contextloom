# router-suite 插件 —— dsh-routing-suite preset 的 ContextLoom 插件化移植

对标 [dsh-routing-suite](https://github.com/yjh051108/dsh-routing-suite) 的**思维模式路由预设**（preset v0.3.0），
以 **ContextLoom 插件体系 v2** 的脚本形态完整承载其核心机制——插件内容全部为脚本资产，
**零 Go 代码、零内置**，路由/锚定/注入逻辑全部以插件形态存在。

## 移植的机制对照

| dsh-routing-suite preset 机制 | ContextLoom 插件承载 | 脚本 |
|---|---|---|
| **三行为带路由**（spec/react/mixed/weak，按任务特征选 persona） | `hooks.route.script` → 决策注入 system 区 | `scripts/route.sh` |
| **每轮近距离引导/反稀释**（每轮用户消息后注入固定引导） | `hooks.message.inject` → 引导注入 user 消息后 | `scripts/inject.sh` |
| **单任务三锚**（目标锚+收敛锚+反跑题锚） | message 钩子 round=1 注入完整三锚 | `scripts/inject.sh` |
| **AI 自优化路由工具**（dev_router_status / dev_router_mode） | `tools[].script` 真实注册进工具 Registry，模型可调用 | `scripts/router_status.sh` / `scripts/router_mode.sh` |

## 三能力一览

| 能力 | manifest 声明 | 脚本 | 行为 |
|---|---|---|---|
| **路由决策（route 钩子）** | `hooks.route.script` | `scripts/route.sh` | 构建提示词时执行；stdin=JSON `{mode,input,round}`，按任务关键词做三行为带分类（spec=计划者 / react=执行者 / mixed=混合 / weak=模型自分类），输出 persona 提示词段注入 system 区；优先级：用户覆盖（dev_router_mode set）> 关键词自动分类 > mode 兜底 |
| **每轮引导（message 钩子）** | `hooks.message.inject` | `scripts/inject.sh` | 每轮用户消息后执行；stdin=JSON `{input,round,mode}`；round=1 注入单任务三锚（目标锚/收敛锚/反跑题锚），round>1 注入收敛推进引导（反稀释），防多轮主题漂移 |
| **可调用工具** | `tools[].script` | `scripts/router_status.sh` / `scripts/router_mode.sh` | 注册为 `plugin.dev_router_status`、`plugin.dev_router_mode`（schema 完整透传），模型可直接调用；参数 JSON 经 stdin 传入，stdout 为结果 JSON |

## 安装

把整个 `router-suite/` 目录复制到插件目录即可（二选一）：

```bash
# 用户级（个人通用，跨项目共享）
cp -r examples/plugins/router-suite ~/.contextloom/plugins/

# 或项目级（安装位，不在 git 跟踪内，本仓库已预装）
cp -r examples/plugins/router-suite <项目根>/.contextloom/plugins/
```

启动 contextloom 后可见注册日志：

```
已加载 1 个用户级插件
插件工具已注册: plugin.dev_router_status（来自插件 router-suite）
插件工具已注册: plugin.dev_router_mode（来自插件 router-suite）
```

## 验证

```bash
./verify.sh          # 单测：manifest 解析 + 脚本契约（三行为带/三锚/双工具）
```

或手工验证各脚本（stdin 传 JSON）：

```bash
echo '{"input":"请设计登录模块架构","round":1,"mode":"task"}' | scripts/route.sh   # spec 计划者
echo '{"input":"修复登录接口 bug","round":1,"mode":"task"}'   | scripts/route.sh   # react 执行者
echo '{"input":"实现登录","round":1,"mode":"task"}'            | scripts/inject.sh  # 三锚
echo '{"mode":"query"}'                                        | scripts/router_mode.sh
echo '{"mode":"set","value":"spec"}'                           | scripts/router_mode.sh  # 切换后 route 优先采用
```

## 契约参考

完整字段说明见 `插件开发接入手册.md` §3.2（tools）/ §3.3（hooks）/ §4.4（message）/ §4.5（route）/ §4.6（工具注册）。
