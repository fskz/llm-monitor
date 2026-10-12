# dev..master 审查修复包

> 来源：2026-10-12 /grill-me 审查（对象=dev..master 全部差异 75 文件，处置=发现全量修复，口径=bug+性能+简化）。
> 审查报告全文即需求基线；本 PRD 按可验收粒度转写，编号沿用报告。

## Goal

修复审查发现的 3 个 P0/P1 崩溃级/死锁级 bug、6 个 P2/P3 缺陷，落地 8 项简化/复用与 1 组性能聚合改造；全程不改变既有功能语义（除非审查项本身就是语义缺陷，如 #8 文案）。

## Requirements

### R1 P0/P1 bug（崩溃/死锁）

- **#1 TUI 概览递归栈溢出**：`refresh()` 内 `SetCurrentItem` 同步触发 changed 回调 → `selectProvider` → `redrawLocked` → `refresh` 无界递归。修法：重入守卫（refreshing 标志或恢复光标前摘回调），≥2 个 provider 按 ↓ 不再崩溃。
- **#2 删除确认无取消路径**：`confirmDelete` 的 `SetDoneFunc` 只处理 idx==0；Esc(-1)/取消按钮(1) 必须也走 dismiss。取消路径不触碰 store。
- **#3 web 表单 step 阻塞**：interval 秒→分钟、ms→秒换算产生非整数步进值（如 90s→1.5min、10050ms→10.05s），浏览器 step-mismatch 拦截提交。修法：放宽 step（interval/`f-ttft`/`f-slow`/`f-timeout`/`s-*` 系列相应处理），保持既有校验语义不变。

### R2 P2 资源泄漏/一致性

- **#4 `openBrowser` 无 `Wait()`**：`cmd.Start()` 后异步 `Wait()` 回收，避免僵尸进程。
- **#5 退出后 `QueueUpdateDraw` 永久阻塞**：ticker 循环与后台探测的 UI 更新在 app 退出后不再永久阻塞（监听 done 或非阻塞发送）。
- **#6 报告连败徽章硬编码阈值 2**：改读 settings.streak_alert（RenderReport 已持有 store），与 web/TUI 一致。
- **#7 `loadOverview` 错误耦合**：`/api/settings` 失败不连累已取到的 providers 渲染；settings 单独 try/catch，失败沿用现值，下次轮询重试。

### R3 P3 文案

- **#8 报告文案与口径不符**：空样本提示（report.tmpl:93）不再断言"定时探测"；页脚（:117）改为与现行口径一致（手动样本默认计入比例，说明来源过滤）。措辞与 REQUIREMENTS.md 对齐。

### R4 简化/复用（不改变行为）

- **S1 设置校验单一所有者**：`server.validateSettings` 与 `tui.validateSettings` 合并为一个所有者（view 层或 store 层），两处调用点删除本地拷贝；错误文案不变。
- **S2 `scrubKey` → `view.ScrubSecret`**：删除 TUI 本地重复实现，直接调用 view 已导出版本。
- **S3 Up/Down 表单导航合一**：`formDialog.arrowNavigation` 与 `arrowNavCapture` 收敛为一个共享助手。
- **S4 死代码清理**：删除 `monitorStatusOf`、`currentURL`、`ui.mu`（含其 sync 导入若无他用）、`osMkdirAll`/`osStat` 未用桩。
- **S5 恒真/冗余条件**：`overviewKeys` 的 `(rune==keyNew || id>0) && rune==keyNew` 化简；`globalKeys` 中 keySettings/keyToggle 的死 `modalOpen` 分支删除；`forms.go` 的 `if d := ...; true {}` 展开为普通赋值。
- **S6 `intervalStr` 内联**：删除透传包装，调用点直用 strconv.Itoa。
- **S7 web 默认值合并**：`openProviderDialog` 的七元三元式改一次展开合并（`{...defaults, ...p}` 形态），fallback 常量保留但集中一处。
- **S8 TUI 格式化函数收敛**：`fmtMs/fmtTPS/fmtTPOT/fmtFlip` 与 view/report.go 的四个同义函数合并（view 导出、TUI 委托）；文案/边界值不变。

### R5 性能（5s 轮询路径的快照乘数）

- **PERF1 单快照聚合**：store 增加聚合方法（一次 snapshot、一遍过滤填充 stats+streaks+percentiles+flip+errors 等指标），`ViewOf`、TUI `statsBlock`、`handleStats`、`RenderReport` 共用；各调用点的结果数值与现行为完全一致（这是重构不是口径变更）。
- **PERF2 `resultsTable` 重复 `viewOf`**：传入 renderText 已算好的 `v`，消除重复（顺带消除同帧版本标签竞态）。
- **PERF3 `streakThreshold` 循环外提**：`overview.refresh()` 每 provider 拿一次全局锁 → 循环外取一次。

## Out of Scope

- web `appSettings` 陈旧问题（#9）——记录为已知行为，本轮不动轮询协议。
- `fmtMs` 系 JS 侧第三份拷贝——跨语言无法共享代码，仅 Go 侧收敛（S8）。
- monitorColor 与 STATUS_META 颜色映射的契约测试——单独改进项，不并入本包。

## Acceptance Criteria

- [ ] A1 两个 provider 的 TUI 会话中按 ↓ 多次，进程不崩溃（手动验证 + 重入守卫单测）。
- [ ] A2 删除确认模态中按 Esc 与点「取消」均关闭模态，store 无变更（单测覆盖 idx=1 与 -1）。
- [ ] A3 interval_sec=90 的 provider 在 web 编辑表单可原样保存（step 修正后无约束校验拦截）。
- [ ] A4 `go vet ./...` 与 `go test ./... -race` 全绿（含既有测试）。
- [ ] A5 grep 确认 `monitorStatusOf|currentURL|scrubKey|intervalStr` 无残留定义；`validateSettings` 全库仅一个所有者实现。
- [ ] A6 报告导出（streak_alert=5、连败 3 的场景）不显示连败徽章；streak_alert=2 时显示——与面板口径一致。
- [ ] A7 `/api/settings` 返回 500 时 web 概览仍渲染 providers（代码审查确认分离的 catch）。
- [ ] A8 PERF 重构后既有统计相关测试全绿即视为数值等价（stats/percentiles/streak 系单测是等价性证据）。

## 验证命令

```
go vet ./... && go test ./... -race
```
