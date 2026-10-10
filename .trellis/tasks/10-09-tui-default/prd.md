# TUI 默认模式 + 按需 Web 面板(零常驻端口)

> 来源:用户请求"提供一个无需占用监听端口的改造方案"(2026-10-09),经 grill-me 拷问逐分支决议。
> 痛点定性:反感常驻进程长期持有 HTTP 监听端口(非端口冲突扫描问题)。

## Goal

默认运行形态改为 tview TUI,**零常驻监听端口**;现有浏览器面板全功能保留,但仅由 TUI 内快捷键按需拉起(固定起始端口扫描 + 手动关闭);一步到位迁移,同步改写 REQUIREMENTS.md。

## Background / Confirmed Facts(代码取证)

- 单进程 = 引擎(每 provider 一个调度 goroutine)+ HTTP 服务(`cmd/llm-monitor/main.go:77-123`);启动即 `findListener` 从 10110 向后扫 100 端口并自动开浏览器。
- web 面板是纯轮询消费者(`web/app.js:503-505`,5 秒 setInterval),无 WebSocket/SSE,服务端不持面板状态 → 面板可随服务按需启停。
- `/api/report` 已能渲染自包含单文件 HTML(`internal/server/report.go:133`),但其入口绑在 `http.ResponseWriter` 上。
- 存储为 `config.json` + `results/*.jsonl`,**无任何文件锁** → 两进程并发写同一数据目录不安全,这是"单进程单实例"决议的根据。
- go.mod 零第三方依赖;TUI(tview)将是第一个重依赖(需修订 go-conventions.md 的零依赖条款为"无特殊理由不加依赖,tview 为已批准例外")。
- 状态文案映射是跨层契约(go-conventions.md:probe/store/web/app.js STATUS_TEXT + report.go reportStatusText 四处同步),TUI 加入后必须下沉共享,否则变六处。

## Requirements

### R1 入口与进程模型

- 裸运 `llm-monitor`(含 Windows 双击)→ 直接进入 TUI,**全程不监听任何 TCP 端口,不自动开浏览器**。
- 单进程单实例:TUI 进程持有引擎与 store;按需 web 与 TUI 同进程,由 TUI 内快捷键拉起/关闭。
- 不设 `llm-monitor web` 子命令(避免跨进程并发写数据目录,无需文件锁)。
- 不支持无交互终端场景(nohup/开机自启/SSH 掉线即停):监控进程与交互终端强绑定,为明确产品取舍,写入需求文档。
- `--port` 旗标保留,语义改为"TUI 内快捷键拉起 web 时的起始扫描端口"(缺省仍 10110,向后 +1 扫描逻辑复用 `findListener`)。

### R2 按需 Web 面板

- TUI 内快捷键拉起:从 `--port` 起始端口扫描绑定 127.0.0.1,成功后打开系统浏览器并显示实际 URL;同源校验沿用现有逻辑(用实际绑定端口)。
- TUI 内快捷键手动关闭(或退出 TUI 时随进程关闭);**不做闲置自动关闭**。
- web 已在运行时再次按快捷键:提示已在运行 + URL,不重复拉起。

### R3 TUI 功能(与 web 面板全功能对等)

- 双栏布局:左 provider 列表(状态徽章),右详情区(概览 + 统计 + 趋势 + 结果表);上下键切换 provider。
- 概览/统计:与 web 同口径(viewOf/statsView 组装逻辑下沉共享)。
- 趋势图:半块字符 sparkline(成功率/延迟),空数据"暂无样本"。
- 结果表:时间窗/版本/来源筛选 + 分页(复用 store QueryResults 游标)。
- 手动探测:触发 ProbeNow,展示在途/结果;409 语义(在途冲突)以提示呈现。
- provider 增删改:tview 表单,校验规则与 server 端完全一致(400 中文校验消息同源)。
- 报告导出:复用现有渲染管线(同模板+SVG),写磁盘文件,TUI 内提示完整路径。
- 刷新:5 秒定时重读 store(与 web 同节奏),重绘保留选中行/滚动位置。

### R4 代码组织

- 视图组装(`viewOf`/`statsView`/状态文案映射/校验规则)从 `internal/server` 下沉到共享包(如 `internal/view`),server 与 TUI 共用,遵守跨层同步规范。
- 报告渲染管线从 `http.ResponseWriter` 解耦为返回 `[]byte`/写 `io.Writer` 的纯函数,HTTP handler 与 TUI 导出共用。
- `internal/tui` 新包;依赖方向 `cmd → tui → view/store`,`cmd → server → view/store`;tui 不 import server。

### R5 需求文档同步

- REQUIREMENTS.md §5.2(端口行为)、§4(启动/双击场景)、基线 #7(默认端口)改写:默认 TUI 零端口,web 按需拉起。
- go-conventions.md:零依赖条款、包布局图、状态文案映射同步点数更新。

## Acceptance Criteria

- [ ] A1 裸运 `llm-monitor` 进入 TUI;进程全生命周期 `ss -tlnp`/`netstat` 验证无 TCP 监听;不打开浏览器。
- [ ] A2 TUI 内拉起 web 快捷键:监听 127.0.0.1(起始 `--port`,被占向后 +1),浏览器打开,URL 展示;再按提示已在运行;手动关闭快捷键后端口释放。
- [ ] A3 TUI 内可完成:查看总览/详情统计、切时间窗/版本/来源、翻页结果表、手动探测、增删改 provider(校验拒绝与 web 同消息)、导出报告文件到磁盘并提示路径。
- [ ] A4 sparkline 正常渲染,无样本显示"暂无样本";5 秒刷新不丢选中行/滚动位置。
- [ ] A5 导出的 HTML 报告与 web 模式下载的内容同构(同模板同数据);报告含转义与 Key 不泄露(沿用既有 A4 级断言)。
- [ ] A6 状态文案映射在 probe/store/view(共享)/web/report 各处单一定义来源;新增测试:共享映射与 web STATUS_TEXT 语义一致(评审检查项)。
- [ ] A7 退出 TUI(Ctrl+C/菜单)触发既有优雅退出路径:引擎 Shutdown ≤10s、在途探测取消落盘 `cancelled`。
- [ ] A8 全量回归:gofmt/vet/`go test ./... -race` 全绿;REQUIREMENTS.md 与 go-conventions.md 修订完成;go.sum 提交。

## Out of Scope

- 无终端后台/headless 模式;`llm-monitor web` 子命令;多实例并发(文件锁);闲置自动关闭 web;TUI 鼠标支持;CSV/JSON 导出;配置导入导出。

## Technical Notes

- 架构决议树全文见会话记录;实现分层与包结构见 design.md,执行清单见 implement.md。

## Open Questions

- 快捷键具体绑定(拉起/关闭 web、各功能键位)在实现时定,记入 design.md 键位表。
