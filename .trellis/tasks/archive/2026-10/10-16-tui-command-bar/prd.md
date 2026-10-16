# TUI 命令系统:/xxx 命令替代快捷键

> 来源:grill-me 拷问(2026-10-16),参照 /home/fs/projects/mos-runner v1.7 的命令栏设计。Q1–Q11 决议见下;需求与验收以本文件为准,技术方案见 design.md。

## Goal

TUI 底部常驻命令栏成为唯一动作入口:14 条 `/xxx` 命令(其中 3 条带枚举参数)全部替代现有三处 rune 快捷键;前缀过滤菜单 + Tab 补全;浏览结构性键(方向键/Tab/Esc/Enter/翻页/表单导航)保留。

## Background / Confirmed Facts(代码取证)

- 现有 rune 动作键分散三处:全局 `run.go:234 globalKeys`(q/p/r/w/,/x)、列表 `run.go:269 overviewKeys`(n/e/d)、详情 `detail.go:337`(1/2/3/R/S);无命令注册表,主界面无 InputField。
- mos-runner 同栈(tview/tcell)实现成熟:`tui/command.go` 命令表 + 前缀过滤 + 常驻菜单 TextView,四条实测坑见 design.md §6。
- spec 硬约束(`go-conventions.md`):状态栏文案只能来自 `view.StatusText`/`MonitorText`;TUI 回调同步可重入;`Application.Run` 后 `QueueUpdateDraw` 永久阻塞。
- detail 游标分页 pageNext/pagePrev 目前无键绑定,仅测试调用(保持现状,不接命令)。

## 拷问决议(Q1–Q10)

| # | 决议 |
|---|---|
| Q1 | 三处 rune 动作键全删,命令唯一入口;结构性键保留;`q` 删除,退出= `/quit` + Ctrl+C |
| Q2 | 照搬 mos-runner:命令栏常驻底部、启动即默认焦点 |
| Q3 | 14 条命令(见 R1 表);不加 `/page` |
| Q4/Q5 | 支持参数:`/window` `/revision` `/source` 带枚举参数,带参直选、裸执行循环;其余 11 条零参数 |
| Q6 | 命令作用于当前选中项,与焦点无关;未选中不执行 + flash 提示 |
| Q7 | 命令表(含描述)放 internal/tui;状态栏引导与执行反馈文案走 view 层既有通道 |
| Q8 | 同一任务两步走(先命令系统、后删旧键),不留并存期 |
| Q9 | Tab 三站循环:命令框 → 列表 → 详情 → 命令框 |
| Q10 | 底部全宽三段:菜单区固定 3 行(常驻占位)+ 命令框 3 行 + 状态栏 1 行(从右列底移来) |

## Requirements

### R1 命令集(14 条)

| 命令 | 参数 | 裸执行行为 | 替代键 |
|---|---|---|---|
| `/quit` | 无 | 退出(优雅关闭) | q |
| `/probe` | 无 | 探测当前选中项 | p |
| `/report` | 无 | 导出 HTML 报告 | r |
| `/web` | 无 | 拉起/关闭按需 Web 面板 | w |
| `/settings` | 无 | 打开全局设置模态 | , |
| `/toggle` | 无 | 启停当前选中项 | x |
| `/new` | 无 | 新建监测对象(现有表单模态) | n |
| `/edit` | 无 | 编辑当前选中项(现有表单模态) | e |
| `/delete` | 无 | 删除当前选中项(保留确认模态) | d |
| `/window` | `1h`/`24h`/`7d` | 循环切换时间窗 | 1/2/3 |
| `/revision` | `current`/`all` | 循环切换版本过滤 | R |
| `/source` | `scheduled`/`manual`/`all` | 循环切换来源过滤 | S |
| `/refresh` | 无 | 手动全量刷新(新增,现为 5s 自动刷新无手动入口) | — |
| `/help` | 无 | 菜单/日志区列出全部命令与描述 | — |

### R2 命令栏与菜单交互(照搬 mos-runner)

- 命令栏常驻底部,label ` > `,启动即默认焦点;非 `/` 开头文本 Enter 不执行(吞掉,防误触)。
- 输入 `/` 即弹全量命令菜单;前缀过滤(非模糊);菜单项 = 命令名 + 一句话描述,↑↓ 循环移动选中,Tab 补全,Enter 执行选中/当前输入,Esc 清空关闭。
- 空格后切参数候选(仅 3 条过滤命令):同一菜单区展示参数枚举,同样前缀过滤/↑↓/Tab/Enter;菜单内选中参数项 Enter=直选执行。
- 裸命令(如 `/window`,无空格无参数)Enter = 按 R1 矩阵裸执行(循环切换)——Q5 决议的兜底语义。
- 参数非法(如 `/window 9h`)不执行,flash 提示合法值集。
- 未知命令 flash"未知命令,输入 / 查看全部"。

### R3 上下文语义

- `/probe` `/toggle` `/edit` `/delete` `/window` `/revision` `/source` 作用于当前选中 provider,与焦点无关;未选中时 flash"请先选择监测对象"。
- `/new` `/settings` `/report` `/web` `/refresh` `/quit` `/help` 与选中项无关。
- `/new` `/edit` 沿用现有表单模态;`/settings` 沿用设置模态;`/delete` 保留删除确认模态。

### R4 键位清理

- 删除 globalKeys(q/p/r/w/,/x)、overviewKeys(n/e/d)、detail inputCapture(1/2/3/R/S)的全部 rune 动作分支。
- 保留:↑↓/j/k 列表导航、Tab 三站焦点轮转(Q9)、Esc、Enter、PgUp/PgDn 详情滚动、模态内表单导航、Ctrl+C。
- `keys.go` 常量表退役,由命令表取代;状态栏引导文案改为"输入 / 查看命令"类。

### R5 文案归属(spec 对齐)

- 命令注册表(名称/参数/描述/handler)放 `internal/tui/command.go`,描述是唯一随 tui 包走的用户可见文案。
- 状态栏引导文案:扩展 `view.StatusText` 生成,不在 TUI 硬编码。
- 命令执行反馈(未知命令/参数非法/请先选择/已导出等):复用现有 probe/export 的 status/flash 通道,不新造机制。

## Acceptance Criteria

- [ ] A1 启动后焦点在命令框;输入 `/` 弹全量 14 条菜单;前缀过滤(输入 `/w` 只剩 `/web` `/window`)(模拟屏/单测)。
- [ ] A2 Tab 三站循环 命令框→列表→详情→命令框;菜单开时 Tab=补全不跳焦点(单测)。
- [ ] A3 `/window 24h` 直选生效;`/window` 裸执行循环切换;`/window 9h` flash 合法值集不执行(单测)。
- [ ] A4 `/edit` `/delete` `/toggle` `/probe` 作用于当前选中项(详情区聚焦时同样生效);未选中 flash(单测)。
- [ ] A5 旧 rune 键全部失效:q/p/r/w/,/x、n/e/d、1/2/3/R/S 在对应焦点下不再触发动作(回归测试改写)。
- [ ] A6 状态栏引导文案与执行反馈全部来自 view 层;`grep` internal/tui 无新增硬编码状态文案(除命令描述)。
- [ ] A7 `/quit` 走优雅关闭,`TestRunReturnsOnQuitKey` 改写为命令链路后通过;`gofmt`/`go vet`/`go test ./... -race` 全绿。
- [ ] A8 冒烟(用户):实际终端走通 命令输入→菜单选择→参数候选→执行→反馈 全链路;模态打开时命令框不抢输入。

## Out of Scope

- 游标分页命令(`/page`);命令历史 ↑ 回溯;鼠标支持;模糊匹配;web 面板入口改造。

## Open Questions

- 无(Q1–Q11 已全部拷问锁定)。
