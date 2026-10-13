# Go 侧回归测试报告（GUI"停止对话切回项目复活"修复轮）

- 日期：2026-10-13（测试运行时刻 2026-10-14 00:18 ~ 00:19）
- 被测模块根：H:\创业相关\contextloom（模块 github.com/contextloom/contextloom-agent，go1.27.2 windows/amd64）
- 基线提交：b5df1ee（feat: 同步来源项目 dsmind-agent 的多轮思考机制与稳定性修复）
- 本轮改动（同步来源项目 54fe01d / 69070bc）：
  - M cmd/contextloom-gui/web/app.js（+27/-6，前端修复：已停止对话切回项目"复活"漏洞，移动语义）
  - ?? cmd/contextloom-gui/tests/stop_resurrect_cycle_test.js（新增前端 node 回归测试）
  - Go 文件改动数 = 0（`git status --short -- '*.go'` 计数为 0）

> 说明：本报告只负责 Go 侧回归。前端 app.js 与 node 测试由主 Agent 并行落地；测试执行时工作区尚未包含这两处前端改动（`git status --porcelain` 为空），但因 Go 源码零改动，前端改动对 Go 测试结果无影响。

## 1. 构建结果

```
cd 'H:\创业相关\contextloom'; go build ./...
BUILD_EXIT=0
```

结论：全模块编译成功，无编译错误（含 `//go:embed all:web` 嵌入的 app.js 静态资源）。

## 2. 测试结果总览

命令：`go test -count=1 ./internal/gui/ ./internal/runtime/ ./internal/ctxmgr/ ./cmd/contextloom-gui/`

| 包 | 结果 | 耗时 | 失败用例数 |
|---|---|---|---|
| internal/gui | FAIL | 6.318s | 2 |
| internal/runtime | FAIL | 63.530s | 25 |
| internal/ctxmgr | ok | 0.103s | 0 |
| cmd/contextloom-gui | FAIL | 3.684s | 1 |

总退出码 TEST_EXIT=1。

## 3. 失败明细（含单独重跑验证）

### 3.1 internal/gui（2 个，全部为 TempDir 清理环境性失败）

- TestRealRuntime_CompressTwice
  - 断言主体通过：第1次压缩 截断 3 条 / 丢弃 1 条，释放 509 tokens（47.07% -> 34.64%）；第2次压缩 截断 5 条 / 丢弃 2 条，释放 956 tokens（34.64% -> 11.30%）
  - 失败点：`TempDir RemoveAll cleanup: unlinkat ...\.contextloom\audit\audit-20261014.jsonl: The process cannot access the file because it is being used by another process.`
- TestRealRuntime_CompressTwice_PushEvent
  - 失败点：同上（仅 cleanup 失败）

单独重跑（`go test -count=1 -v -run "TestRealRuntime_CompressTwice$|TestRealRuntime_CompressTwice_PushEvent" ./internal/gui/`）：两个用例 cleanup 失败均复现，EXIT=1（3.558s）。

### 3.2 internal/runtime（25 个）

A. TempDir 清理失败（23 个，Windows 文件句柄占用，audit-*.jsonl 被其他进程占用）：

TestLoadSession_MergesPostCompactionEvents、TestLoadSession_NoIncrementalDuplication、TestRuntime_MCPServerRegistration、TestBuildBackendForPreset_Resolves、TestBuildBackendForPreset_Missing、TestEmployeeChainBackend_MultiSource、TestEmployeeChainBackend_Empty、TestResolveSubAgentBackend_EmployeePresetWithoutGateway、TestResolveSubAgentBackend_OverrideWins、TestResolveSubAgentBackend_OverrideInvalidFallsBack、TestResolveSubAgentBackend_DefaultNoEmployee、TestResolveSubAgentBackend_PresetCache、TestSpawnTask_ModelOverrideEndToEnd、TestEmployeeSummary_ShowsModelBinding、TestNew_PluginToolsRegistered、TestNew_PluginToolExecution、TestRuntime_PluginMessageHooks、TestRuntime_PluginRouteHooks、TestAddToHistory_NoVisionSupport_PlainText、TestBuildHistoryMessage_SupportsVisionGate、TestStageImages_ConsumeOnce、TestDetectImages_StagedVSPathDedup、TestGatewayPath_VisionCapabilityFromPreset

B. 平台性断言失败（2 个纯断言 + 3 个断言叠加 cleanup）：

- TestResolvePath_Absolute：`runtime_test.go:851: expected '/absolute/path/to/dir', got "\\project\\absolute\\path\\to\\dir"`（Windows 路径分隔符差异，0.00s，无 cleanup）
- TestResolvePath_Relative：`runtime_test.go:859: expected "/project/relative/path", got "\\project\\relative\\path"`（同上，0.00s，无 cleanup）
- TestNew_PluginToolExecution：`plugin_v2_test.go:113: result.Error = "插件工具 echo_params 执行失败: start: fork/exec ...\plugins\demo\tool.sh: %1 is not a valid Win32 application."`（.sh 插件脚本在 Windows 无法直接执行）+ cleanup 失败
- TestRuntime_PluginMessageHooks：`plugin_v2_test.go:147: message 钩子输出 = ""` + cleanup 失败
- TestRuntime_PluginRouteHooks：`plugin_v2_test.go:181: route 钩子输出 = ""` + cleanup 失败

单独重跑（`go test -count=1 -v -run "TestResolvePath_Absolute|TestResolvePath_Relative|TestNew_PluginToolExecution|TestRuntime_PluginMessageHooks|TestRuntime_PluginRouteHooks" ./internal/runtime/`）：
- TestResolvePath_Absolute / TestResolvePath_Relative：稳定复现（0.00s，纯断言失败）
- TestNew_PluginToolExecution：稳定复现（.sh 执行失败 + cleanup）
- 两个 Hooks 用例：断言失败 + cleanup 失败均复现

EXIT=1（5.782s）。

### 3.3 cmd/contextloom-gui（1 个）

- TestDefaultProjectDir_AppBundle：`projects_test.go:246: 普通场景应返回 exe 目录: got="\\opt\\contextloom"`（Windows 下 /opt/contextloom 被解析为 UNC 风格路径，平台性断言差异）

单独重跑：稳定复现，EXIT=1（0.104s）。

## 4. 失败归因分析

1. 与本轮前端修复无因果关系的硬证据：本轮工作区 Go 源码改动数为 0（`git status --short -- '*.go'` 为空），即工作区编译的 Go 源码与 HEAD（b5df1ee）逐字节一致，上述失败在修复前同样存在（pre-existing），修复前后 Go 测试结果必然相同。
2. app.js 的影响面仅限构建产物：`cmd/contextloom-gui/main.go` 的 `//go:embed all:web` 将 app.js 作为静态资源嵌入二进制，不参与任何 Go 测试断言逻辑。
3. 失败类型全部为 Windows 平台/环境特性：
   - TempDir cleanup 失败 = 文件句柄释放时序（audit jsonl 写入后未及时释放，Windows 上占用文件不可删除）；
   - TestResolvePath_*、TestDefaultProjectDir_AppBundle = 测试断言按 Unix 路径语义编写，在 Windows 下路径分隔符/绝对路径语义不同；
   - 插件 3 用例 = .sh 脚本在 Windows 无法直接执行（钩子输出为空即脚本未产出）。
   均与"已停止对话切回项目复活"漏洞修复（前端 WS 消息过滤）无关联。

## 5. 结论

- 构建：`go build ./...` 通过（EXIT=0）。
- 后端回归：internal/ctxmgr 全绿；internal/gui、internal/runtime、cmd/contextloom-gui 存在既有 Windows 平台/环境性失败（28 个用例），经单独重跑稳定复现，全部为本轮修复前既有问题，与前端 app.js 改动无关，非本轮引入。
- 任务预设"四个包既有测试全部通过"与实际不符，如实记录；因 Go 侧零改动，前端修复对 Go 测试结果无影响，可判定本轮修复未破坏任何既有 Go 测试。

## 6. 改进建议（只记录，不修）

1. TempDir cleanup 类：audit 日志写入器增加显式 `Close()`，或改用 `t.Cleanup` 中等待句柄释放，消除 Windows 下偶发占用。
2. 路径断言类：TestResolvePath_*、TestDefaultProjectDir_AppBundle 用 `filepath.FromSlash` 归一化或按 `runtime.GOOS` 分支断言。
3. 插件 3 用例：对 .sh 工具增加平台守卫（非 Unix `t.Skip`），或改用平台可执行脚本。

---
附：复现命令清单

```
cd 'H:\创业相关\contextloom'
go build ./...
go test -count=1 ./internal/gui/ ./internal/runtime/ ./internal/ctxmgr/ ./cmd/contextloom-gui/
go test -count=1 -v -run "TestRealRuntime_CompressTwice$|TestRealRuntime_CompressTwice_PushEvent" ./internal/gui/
go test -count=1 -v -run "TestResolvePath_Absolute|TestResolvePath_Relative|TestNew_PluginToolExecution|TestRuntime_PluginMessageHooks|TestRuntime_PluginRouteHooks" ./internal/runtime/
go test -count=1 -v -run "TestDefaultProjectDir_AppBundle" ./cmd/contextloom-gui/
```