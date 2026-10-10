# 执行计划:TUI 默认模式 + 按需 Web 面板

> 前置:prd.md(需求/验收)、design.md(技术设计)。按阶段推进,每阶段末有验证门。

## 阶段 0:依赖引入

- [x] `go get github.com/rivo/tview`;确认 `go mod tidy` 后 go.sum 干净(tools.go build-tag 固定,阶段 4 tui 直接 import 后删除)
- [x] 验证:`go build ./...` 通过

## 阶段 1:视图下沉(internal/view)

- [x] 从 server 迁出 `providerView`/`viewOf`/`statsView`/`statsViewFrom` → internal/view
- [x] 迁出并导出 `StatusText`/`MonitorText`(report.go 的 reportStatusText/reportMonitorText)
- [x] 迁出 provider 校验(400 中文消息规则)为 `view.Validate*`(ProviderForm.Prepare/Validate)
- [x] server 改调 view;删除 server 内重复定义
- [x] 测试迁移:server 现有视图/校验断言改指向 view 包;view 内新增 web/app.js STATUS_TEXT 同步契约测试(A6)
- [x] 验证门:`go test ./... -race` 全绿;server 包无残留文案映射

## 阶段 2:报告渲染纯函数化

- [x] `RenderReport(w io.Writer, ...)` 与 handler 解耦;模板/report_chart/SVG 迁入 view;slug 与文件名导出
- [x] 渲染断言改走 bytes.Buffer
- [x] 验证门:报告内容与重构前同构(现有 report 测试全绿)

## 阶段 3:main 重构(去常驻监听)

- [x] 删除启动即 listen/openBrowser;`findListener`/`openBrowser` 迁至 internal/tui/webctl.go
- [x] main 装配改为 `tui.Run(ctx, deps)` 阻塞;退出走既有 eng.Shutdown 路径;EngineAPI/ProviderMutator 接口上移 view 包
- [x] `--port` 语义更新(帮助文本:按需 web 起始端口)
- [x] 验证门:A1 无头验证(tcell 模拟屏测试:ctx cancel/q 键退出;真人终端冒烟待用户)

## 阶段 4:TUI 主体(internal/tui)

- [x] 4a run.go:装配 + 主循环 + 5s Ticker QueueUpdateDraw(选中/滚动保持)
- [x] 4a overview.go:左栏列表 + 状态徽章着色
- [x] 4a detail.go:概览 + 统计 + 结果表(筛选/游标分页)
- [x] 4a sparkline.go:半块字符渲染 SeriesBucket,空数据占位
- [x] 4a keys.go:读侧键位落地(design §7;动作键位留待 4b)
- [x] 4b forms.go:增删改表单 + view.Validate* + 删除确认(openForm/confirmDelete 模态)
- [x] 4b 手动探测 p 键:ProbeNow goroutine + 在途/冲突/结果状态行(actions.go probeNow)
- [x] 4b report.go:r 键导出到磁盘(<data>/reports/,actions.go exportReport)+ 路径提示
- [x] 4b webctl.go:w 键拉起/关闭按需 web(webPanel;ServerHook 依赖注入避免 tui→server;状态栏 URL;已运行幂等)
- [ ] 验证门:A2-A5 手动冒烟清单逐项过(需用户真实终端)

## 阶段 5:文档同步 + 全量回归

- [x] REQUIREMENTS.md:§5.2 端口条款(零监听/按需拉起/无终端不支持)、§4.3 改为 TUI 默认 + Web 按需、§2 典型流程、§12.2-2/4、基线 #7 修订、决策 #13 新增、§10 API 表注(按需服务期间可用)
- [x] go-conventions.md:包布局图(view/tui)、零依赖→tview 例外、状态文案同步点(view 单源 + app.js 契约测试)、进程/UI 拓扑节、TUI conventions 节、报告导出节改 view.RenderReport 双前端、Server 节改按需语义、校验单一 owner
- [x] 全量:`gofmt -l` 空、`go vet ./...` 清洁、`go test ./... -race` 全绿
- [x] 机械验收 13 项:依赖方向×3、启动零监听、无硬编码映射、校验单入口、无陈旧引用、findListener 唯一、优雅退出、契约测试、cursor 语义对齐、文案一致、filter 映射
- [ ] 验证门:A2-A5 真实终端冒烟(用户执行)

## 回滚点

- 每阶段独立可编译可测;任一阶段失败可 `git checkout` 该阶段起点。整个改造单分支,revert 即回旧默认。

## 评审门

- 阶段 1 后:view 包边界评审(是否混入 HTTP 语义)
- 阶段 4 后:键位表与交互走查(用户确认)
