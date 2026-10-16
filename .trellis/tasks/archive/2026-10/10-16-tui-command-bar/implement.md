# 执行计划:TUI 命令栏(两步提交序列,Q8)

## 第一步:命令系统落地(旧键暂并存)

- [ ] 1 view 层文案:状态栏引导(StatusText 体系)+ 命令反馈常量(未知命令/参数非法/请先选择/无参数)→ 单测
- [ ] 2 `internal/tui/command.go`:14 条注册表 + filter(命令前缀/参数候选/关闭三态)+ handleKey(↑↓/Tab/Enter/Esc)+ execute(解析/校验/分发/未选中拦截)+ menuText → `command_test.go` 全分支单测(A1/A3/A4 逻辑半)
- [ ] 3 `run.go` 布局改造:底部全宽三段(menuView 3 行常驻 + cmdInput + statusbar 迁移)、启动焦点 cmdInput、globalKeys 重写为 Tab 三站轮转 + modalOpen 守卫 → 模拟屏测试(A1/A2)
- [ ] 4 handler 接线:/window /revision /source setter 收拢(detail.go)、/refresh 触发现有刷新管线、其余复用现有函数(design §4)
- [ ] 5 门:gofmt / go vet / `go test ./... -race`;冒烟:命令输入→菜单→参数候选→执行→反馈

## 第二步:删旧键 + 测试改写 + 文档

- [ ] 6 删 globalKeys 全部 rune 分支、overviewKeys、detail.inputCapture、`keys.go`;状态栏引导文案换新(A5)
- [ ] 7 测试改写:旧键位测试删除,`TestRunReturnsOnQuitKey` 改为 `/quit` 注入链路(A7);新增 rune 穿透断言
- [ ] 8 文档:REQUIREMENTS 键位表→命令表、README 操作说明;spec 更新走 3.3(go-conventions TUI conventions 命令栏节)
- [ ] 9 全量门:gofmt / vet / `go test ./... -race`;用户冒烟 A8(实际终端全链路 + 模态不抢输入)

## 回滚点

- 第一步后:revert 提交①即回现状(命令栏从未暴露给用户前也可整步重做);
- 第二步后:revert 提交②回"并存态"(仅任务内过渡态,不对外发布)。
