# 技术设计:TUI 默认模式 + 按需 Web 面板

> 决议来源:grill-me 拷问(2026-10-09)。本文件只写技术方案;需求与验收见 prd.md。

## 1. 目标架构

```
cmd/llm-monitor ──→ internal/tui ──→ internal/view(新,共享视图组装)
      │                 │                  ↑
      │                 └──→ internal/store│
      │                                    │
      └──→ internal/server ────────────────┘
              (按需拉起,与 TUI 同进程)
```

- 依赖方向不变原则:`server`/`tui` 都不 import `engine`,经窄接口由 cmd 适配(现有 `EngineAPI`/`ProviderMutator`/`engineAdapter` 模式原样延用)。
- `internal/view` 承载原 server 内的视图组装与文案映射:
  - `viewOf`/`providerView`/`statsView`/`statsViewFrom`(views.go、stats_api.go 中迁出)
  - `ReportStatusText`/`ReportMonitorText`(report.go 中迁出,导出)
  - provider 校验函数(400 中文消息规则,providers.go 中迁出)
  - 报告渲染:template 实例随 view 包初始化,`RenderReport(w io.Writer, data)` 纯函数化
- server 包保留 HTTP 语义层(handler、sameOrigin、路由、`http.ResponseWriter` 细节),调用 view 完成组装。

## 2. main 重构

```
main()
 ├─ flag:--port(默认 10110,现为 web 拉起起始端口)
 ├─ dataDir/store.New(不变)
 ├─ engine.New + eng.Start(不变)
 ├─ tui.Run(ctx, tui.Deps{Store, EngineAPI, Mutator, Port, WebFS}) ← 阻塞主 goroutine
 │    └─ 退出时返回 → 走既有优雅退出路径
 └─ eng.Shutdown(10s)(不变)
```

- 删除:启动即 `findListener` + `openBrowser` + 常驻 `http.Serve`。
- `findListener`/`openBrowser` 移入 tui 包(或留 cmd 由依赖注入传入),仅在拉起 web 时调用。

## 3. 按需 web(tui 内嵌 httpServer 生命周期)

- 拉起:快捷键 → `findListener(portStart)` → `server.New(st, eng, mut, web.FS(), actualPort)` → goroutine `http.Serve(ln, srv.Handler())` → `openBrowser(url)`;TUI 状态栏显示"web: http://127.0.0.1:<p>/ (w 关闭)"。
- 关闭:快捷键 → `ln.Close()`(在途请求由 `http.Serve` 返回 `ErrUseOfClosedConnection` 吸收)→ 状态栏恢复"web: 未启动"。
- 端口释放即彻底释放,无 linger;同源校验用 `server.New` 收到的实际端口,零改动。
- 再按拉起键:已运行 → 状态栏提示当前 URL,不重复绑定。

## 4. TUI 结构(internal/tui)

```
internal/tui/
  run.go        Run(ctx, deps):应用装配、tview.Application、主事件循环
  overview.go   左栏 provider 列表(状态徽章着色,复用 view.StatusText)
  detail.go     右栏:概览卡 + 统计 + sparkline + 结果表(tview.Table 分页)
  sparkline.go  半块字符 ▁▂▃▄▆▆█ 渲染 store.SeriesBucket
  forms.go      provider 增删改表单(tview.Form),校验调 view.Validate*
  report.go     导出:调 view.RenderReport 写文件,弹出路径提示
  webctl.go     按需 web 拉起/关闭(findListener/openBrowser/httpServer)
  keys.go       键位表(集中定义,见 §7)
```

- 刷新:tview `SetChangedFunc` 外用 `time.Ticker(5s)` → `app.QueueUpdateDraw` 重读 store 并局部更新;选中 provider id 与表格滚动偏移在重绘前保存恢复。
- 手动探测:调 `EngineAPI.ProbeNow`(阻塞式,goroutine 中跑),结果经 `QueueUpdateDraw` 呈现;`IsInFlight` → 提示"探测进行中"。
- 键入安全:provider 名/错误串直接进 tview 文本,tview 不解释 HTML,无注入面;Key 永不显示(view 只出 set+掩码)。

## 5. 状态/文案映射单一来源

- 迁移后同步点:probe 常量 → store 镜像 → **view(共享)** → {web STATUS_TEXT, report 模板函数,TUI}。
- view 导出 `StatusText(status) string` 与 `MonitorText(status) string`;report.go 的模板 funcs 与 tview 徽章、web/app.js 各自薄封装指向同一语义(评审项 A6:三处消费方对同一 status 的输出必须一致)。

## 6. 依赖与构建

- `go get github.com/rivo/tview`(带 tcell 树,约 10+ 间接包);go.sum 首次提交。
- go-conventions.md "零第三方依赖"条款修订:"不因标准库可做的事加依赖;tview 是已批准例外(TUI 层)"。
- Windows 双击:console subsystem 不变(TUI 即控制台程序),无需 buildflag 改动;交叉构建矩阵不变。

## 7. 键位表(实现时微调,先立骨架)

| 键 | 作用 |
|---|---|
| ↑/↓ 或 j/k | 切换 provider(左栏)/移动(表单) |
| Tab | 左栏 ↔ 右栏焦点切换 |
| Enter | 详情内:进入结果表交互;表单:提交 |
| p | 手动探测当前 provider |
| e / n / d | 编辑 / 新建 / 删除 provider(确认弹窗) |
| r | 导出 HTML 报告(当前筛选) |
| w | 拉起/关闭按需 web(状态栏显示 URL) |
| q / Ctrl+C | 退出(走优雅关闭) |

## 8. 兼容与回滚

- 一步到位:无旧入口、无兼容 flag;`--port` 语义变化写入 REQUIREMENTS §5.2 与基线 #7。
- 回滚:整个改造为一个分支单 PR;revert 即回到常驻 web 默认。
- 风险:tview 对极旧终端/IME 的兼容问题 → 验收 A1 在实际终端冒烟;若 CJK 输入在表单中有问题,降级方案为表单字段用英文提示+粘贴输入(不改架构)。

## 9. 测试策略

- view 层:纯函数单测(文案映射全覆盖、校验规则与 server 现行 400 消息逐条一致——从 server 现有测试迁移断言)。
- report 纯函数化后:渲染到 bytes.Buffer 断言内容(迁移现有 report 断言,不再起 httptest)。
- tui 层:逻辑抽薄(刷新状态保存恢复、webctl 生命周期单测:拉起→Serve→Close→端口释放);tview 组件本身不做快照测试。
- 回归:`gofmt`/`go vet`/`go test ./... -race`。
