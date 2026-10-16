# 调研:mos-runner 命令系统参照实现 + llm-monitor 现状取证

> 2026-10-16 grill-me 拷问期间由两个探索代理完成。实现时的直接参照;红线已并入 design.md §6。

## mos-runner(参照,同栈 tview v0.42 + tcell v2.8)

核心文件 `/home/fs/projects/mos-runner/tui/command.go`(158 行,可近乎原样移植)、`run.go`(装配/全局键)、`layout.go`(布局/弹窗/handler)。

- **形态**:底部常驻 InputField(label ` > `,边框"命令"),启动即默认焦点(`run.go:57`);输入 `/` 触发菜单。
- **菜单**:常驻固定 3 行 TextView,`menuOpen = strings.HasPrefix(text,"/") && len(menuItems)>0`;前缀过滤 `strings.HasPrefix(c.name, text)`(`command.go:71-82`);选中项 `▸ ` + `[yellow::b]`,`%-22s` 命令名+描述(`command.go:143-158`)。
- **按键**(`command.go:85-138` handleKey,SetInputCapture 挂 InputField):↑↓ 循环移动;Tab 菜单开=补全(`SetText` 触发 ChangedFunc 连锁)/菜单关=焦点轮转,两种都拦截;Enter 无候选 flash"未知命令"、有候选先清空再执行;Esc 清空+关菜单。
- **注册表**:`command{name,desc,run}` + `init()` 装配(闭包引用 layout 方法/helpCmd 互引,var 会成环);12 条全零参数(v1.7 规格 §18.2,`docs/REQUIREMENTS.md:436-475`,自称参考 Claude Code TUI)。
- **快捷键残留**:仅 Tab 焦点轮转(`run.go:126-138`,焦点在命令框时放行)+ 列表 `d` + 表单方向键;v1.7 删光其余单键。
- **带参数**:无实现(刻意零参数)——llm-monitor 的参数链路(空格切候选/枚举校验)为自研扩展。
- **帮助**:/help 打印到日志区;输入 `/` 弹全量菜单即帮助;状态栏常驻引导文案。

### 实测坑(照搬为红线,源注释可查)

1. `command.go:104-107` — InputField 的 Tab 必须控件级拦截,否则默认跳焦点关掉菜单。
2. `command.go:134-136` — 菜单刷新靠 SetChangedFunc 同步驱动;handleKey 里 QueueUpdateDraw 会在字符插入前重画,吃掉输入("/"打不进框)。
3. `layout.go:118-120` — Flex 树每层只允许一个 Focus=true 子项;焦点切换用 `app.SetFocus` 直落控件。
4. `layout.go:133-136` — 菜单不走 Pages 覆盖层(Add/RemovePage 在事件循环内打断 InputHandler 委托链),用常驻 TextView 纯 SetText。
5. `run.go:123-125` — app 级 InputCapture 先于控件执行,全局 Tab 须给命令框让路。

## llm-monitor 现状(改造起点)

- 装配:`cmd/llm-monitor/main.go:72` → `internal/tui/run.go:72 Run`;`run.go:111-113` 三处 SetInputCapture(detail/overview/app);`run.go:108` 单 Pages("main"+"modal" 槽位)。
- 旧键位:全局 `run.go:234-265`(q/p/r/w/,/x)、列表 `run.go:269-295`(n/e/d)、详情 `detail.go:337-362`(1/2/3/R/S;PgUp/PgDn 为 TextView 内建)。常量表 `keys.go:22-37`。
- 可复用 handler:`actions.go`(probeNow/exportReport/toggleCurrent)、`run.go`(openFormModal:304/openDeleteModal:311/toggleWeb:359)、`settings.go:20`、`detail.go`(cycleWindow/cycleRevision/cycleSource:314/323;pageNext/pagePrev 无键绑定,保持现状)。
- 布局:`run.go:102-104` 左列表 28 列 + 右(detail+状态栏);状态文案 `run.go:221-226 renderStatus`。
- spec 约束(`.trellis/spec/backend/go-conventions.md`):状态文案只能来自 view.StatusText/MonitorText;TUI 回调同步可重入(`:190-198`,overviewPane.refreshing 守卫);Run 后 QueueUpdateDraw 永久阻塞(`:199-203`,走 ui.queueDraw/u.quit);测试用 tcell SimulationScreen + readyScreen happens-before。
- 测试现状:仅退出键走真事件循环(`run_test.go:72`),其余直接调 handler;overviewKeys/detail inputCapture 无键级测试(删除成本低)。
