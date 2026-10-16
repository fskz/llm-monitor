# 技术设计:TUI 命令栏(/xxx 命令替代快捷键)

> 决议来源:grill-me 拷问(2026-10-16)Q1–Q10;参照实现 /home/fs/projects/mos-runner `tui/command.go`(同栈 tview/tcell)。本文件只写技术方案;需求与验收见 prd.md。

## 1. 布局改造(Q10)

```
现状 root = Flex(row): list(28列) | Flex(col): detailView + statusbar(右列底部)
新   root = Flex(col):
        main   = Flex(row): list(28列) | detailView        ← 原两栏,不变
        menuView   = TextView,固定 3 行,常驻占位(关闭时空白)  ← 新增
        cmdInput   = InputField,label " > ",带边框(3 行)      ← 新增
        statusbar  = 全宽 1 行                              ← 从右列底迁出
```

- 启动 `app.SetFocus(cmdInput)`(Q2:命令框默认焦点);Flex 树每层仅一个 Focus=true 子项(红线 §6-4)。
- `renderStatus` 目标控件从右列底换为全宽底行,文案内容逻辑不变。
- menuView 固定 3 行 14 条命令装不下时只显示前缀过滤后的可见窗口(过滤后通常 ≤3 条;`/help` 全量列表走 detailView 顶部临时区或状态栏 flash 引导,见 §4)。

## 2. 命令注册表 internal/tui/command.go

```go
type command struct {
    name string              // 含 / 前缀,如 "/window"
    args []string            // 枚举候选;nil/空 = 零参数命令
    desc string              // 菜单/帮助一句话(唯一随 tui 走的用户可见文案,Q7)
    run  func(u *ui, arg string)  // arg == "" 即裸执行
}

var commands = []command{ /* 14 条,见 prd R1 */ }
```

- 装配用 `init()`(照搬 mos-runner:闭包引用 commands 自身——`/help` 遍历表——避免 var 初始化环)。
- 命令名全小写,`-` 分词(无 `/loop-config` 类长名需求,现有最长 `/settings`);`/revision` `/source` 的参数值集与 detail 过滤状态机的枚举一一对应。

## 3. 输入链路(照搬 mos-runner + 参数扩展)

### filter(SetChangedFunc 驱动,每次文本变化)

1. 无 `/` 前缀 → 菜单关;
2. `/name`(无空格)→ `strings.HasPrefix` 前缀过滤命令表 → menuItems;菜单开 = 有候选;
3. `/name␣…`(含空格)→ 精确定位命令(前缀唯一命中);菜单切该命令 `args` 候选,参数部分同样前缀过滤;
4. 菜单渲染 `menuText()`:`▸ ` 高亮选中项,`%-14s` 命令名(或参数名)+ 描述;只 `SetText`,同步 `draw()`。

### handleKey(cmdInput.SetInputCapture)

| 键 | 菜单开 | 菜单关 |
|---|---|---|
| ↑/↓ | 候选循环移动,拦截 | 放行(InputField 单行无行为) |
| Tab | 补全候选到输入框(命令名或参数),拦截 | 放行给 app 级 Tab 轮转 |
| Enter | 见 execute | 见 execute |
| Esc | 清空输入 + 关菜单,拦截 | 清空输入,拦截 |

### execute(Enter)

1. 文本非 `/` 开头 → 吞掉(防误触,不 flash);
2. 解析 `name` 与 `arg`(首个空格切分;多余空白 trim);
3. name 非表内精确匹配 → flash"未知命令,输入 / 查看全部";
4. 零参数命令收到 arg → flash 提示该命令无参数;
5. 带 args 命令:arg=="" → `run(u,"")`(循环切换,Q5 兜底);arg ∉ args → flash 合法值集;合法 → `run(u,arg)` 直选;
6. 执行前:清空输入框 + 关菜单(先清后跑,照搬);
7. 需选中项的命令(/probe /toggle /edit /delete /window /revision /source):无选中 → flash"请先选择监测对象",不执行(Q6)。

## 4. handler 落点(复用现有实现)

| 命令 | 落点 |
|---|---|
| /quit | `u.quit` channel(现有优雅关闭路径) |
| /probe | `actions.go probeNow`(现有) |
| /report | `actions.go exportReport`(现有) |
| /web | `run.go toggleWeb`(现有) |
| /settings | `settings.go openSettingsModal`(现有) |
| /toggle | `actions.go toggleCurrent`(现有) |
| /new /edit | `run.go openFormModal(0 / id)`(现有) |
| /delete | `run.go openDeleteModal(id)`(现有,确认模态保留) |
| /window | detail 裸执行=`cycleWindow`(现有);带参=新增 `setWindow(arg)`(现 1/2/3 的直接赋值路径,收拢为一个 setter) |
| /revision /source | 同构:`cycleRevision/cycleSource`(现有)+ `setRevision/setSource` |
| /refresh | 新增:触发 5s ticker 同一轮刷新函数(对全部 provider 立即执行一遍,复用现有刷新管线与选中保持逻辑) |
| /help | `menuText()` 全量渲染进 detailView 顶部临时区(或独立 help 视图)+ flash 引导;命令源即注册表,无第二份文档 |

- 选中项 id 从 overviewPane 取(现有选择状态);命令执行后焦点保持在命令框(mos-runner 行为),模态类命令自然移交焦点。

## 5. Tab 轮转与旧 capture 退役

- `app.SetInputCapture` 的 globalKeys 重写为**唯一职责**:Tab 三站轮转 `cmdInput → list → detailView → cmdInput`(SetFocus 直落控件),`modalOpen()` 时全部穿透(现有守卫保留);焦点在 cmdInput 时 Tab 放行给控件(菜单补全优先)。
- `overviewKeys` 整体退役(List 内建 ↑↓/j/k 导航保留,无需 capture)。
- `detail.inputCapture` 整体退役(1/2/3/R/S 全删;PgUp/PgDn 是 TextView 内建滚动,无 capture 也生效)。
- `keys.go` 删除;状态栏引导文案改由 view 层生成(§7)。

## 6. 实现红线(mos-runner 实测坑 + 本项目 spec)

1. `SetChangedFunc` 内只同步 `SetText`+`draw()`,**禁 QueueUpdateDraw**(Draw 抢在字符插入前重画,吃掉输入)。
2. 菜单用常驻 TextView **不走 Pages 覆盖层**(Add/RemovePage 在事件循环内打断 InputHandler 委托链,输入失效)。
3. Tab 必须**控件级拦截**(InputField 默认把 Tab 当 finished 跳焦点,会关菜单不补全)。
4. Flex 树每层仅一个 Focus=true;焦点切换 `app.SetFocus` 直落控件,不做树内投机。
5. app 级 capture 先于控件执行:全局 Tab 必须给命令框让路(否则补全失效)。
6. 本项目 spec(`go-conventions.md`):tview 回调同步可重入——命令 handler 若触发 `SetCurrentItem`/列表刷新,必须走现有 `overviewPane.refreshing` 重入守卫;`Application.Run` 返回后 `QueueUpdateDraw` 永久阻塞——后台动作一律 `ui.queueDraw`/`u.quit`。
7. 模态打开时命令框不得抢输入:modalOpen 守卫覆盖 Tab 轮转与命令执行入口。

## 7. view 层文案扩展(Q7)

- 状态栏引导文案(如"输入 / 查看命令(/help) · Tab 切换焦点"):`view.StatusText` 体系新增导出,TUI 不硬编码。
- 命令反馈消息常量放 view 包:未知命令 / 参数非法(格式化拼接合法值集)/ 请先选择监测对象 / 命令无参数;经现有 renderStatus 的消息通道呈现(probe/export 成功提示同通道)。
- 命令描述留在 tui 命令表(与 handler 同源),是唯一例外(prd R5)。

## 8. 测试策略

- `command_test.go` 纯逻辑单测:filter 三态(命令前缀/参数候选/关闭)、menuText、execute 全分支(分发、参数校验、未知命令、无参数命令带参、未选中拦截)。
- 模拟屏(tcell SimulationScreen + readyScreen 模式):启动焦点在命令框;注入 `/` 弹菜单;注入 `/quit`+Enter 走真事件循环退出(改写 `TestRunReturnsOnQuitKey`);modal 打开时 Tab/命令静默。
- 旧键失效(A5):overviewKeys/detail capture 退役后删除其旧测试;globalKeys 新测试断言 rune 全穿透(无消费)、Tab 三站轮转。
- 回归门:`gofmt` / `go vet` / `go test ./... -race`。

## 9. 兼容、回滚与文档

- 一个分支、两步提交序列(Q8):①命令系统落地(旧键暂并存于任务内)→ ②删旧键+测试改写+文档;revert ② 即回到"命令+旧键并存",revert 整分支回旧键形态。
- 文档:REQUIREMENTS 键位表整节替换为命令表;README TUI 操作说明;spec `go-conventions.md` TUI conventions 增补"命令栏"节(命令注册表契约、四坑红线、文案归属),走 trellis-update-spec(Phase 3.3)。

## 10. 文件变更清单

- 新增:`internal/tui/command.go`(注册表+filter+handleKey+execute+menuText)、`internal/tui/command_test.go`
- 修改:`run.go`(布局三段、Tab 轮转、焦点初始化、/refresh、状态栏迁移)、`detail.go`(setter 收拢与导出、inputCapture 退役)、`overview.go`(overviewKeys 退役)、`actions.go`(handler 复用接口微调)、`internal/view/*`(引导与反馈文案)
- 删除:`internal/tui/keys.go`
