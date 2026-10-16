# 全仓库复审修复包(review-fixpack-2)

> 来源:2026-10-16 /grill-me 全仓库审查(对象=dev 全量约 7500 行,四路并行:后端核心 / 服务端+视图 / TUI / Web 前端;处置=逐条拷问裁定,约 25 项修复、8 项归档)。
> 四份审查报告结论即需求基线;本 PRD 按可验收粒度转写,F 编号为本包序号,括注报告来源与严重度。

## Goal

修复裁定进本轮的缺陷:后端 2 项 P2(留存重写违反 §5.3、SSE 容错)、Web 1 项 P1(详情页死循环)+ 已知遗留 #9、TUI 1 项 P1(分页断链)+ 1 项 P2(菜单视窗)、以及拷问升级/顺手批的确定性错误行为项;落地 JS 格式化函数与默认探测提示词的契约测试锁定。全程不改变既有功能语义,除非缺陷本身即错误行为。

## Requirements

### R1 后端核心(store / probe / engine)

- **F1 留存滞后批量裁剪**(P2,§5.3 违规):`results.go` 达上限后当前每次追加都全量重写历史文件。改为 `len > maxKeep + slack` 才触发重写并裁回 `maxKeep`(slack 取 `maxKeep/10`);重写仍与追加同一临界区,锁内语义不变。追加路径回到 O(1)。
- **F2 SSE 容错**(P2+P3):① 空白载荷 `data:` 行(≥2 空格/制表符)按 keep-alive 忽略——`[DONE]` 判断旁用 `TrimSpace` 判空,不再落 `protocol_error`;② 未知 `field:value` 行按 SSE 规范忽略(仅对无字段结构的裸文本/裸 JSON 保留现判定)。
- **F3 后端顺手批**:`settingsLocked` 清洗负数 `interval_sec`(保留 0=仅手动合法);`Stats` 结构体过时注释改为与 source 过滤实际行为一致;`providers.go` 手写 `"ok"` 字面量归契约常量(消灭状态契约第 5 份拷贝);`bucketLayout` 对 `window<=0` 兜底防除零;`ProbeNow` 补 `schedule` 同款 `defer recover`;`probe_test` 的 `sawBody` 跨 goroutine 竞态消除。

### R2 服务端 + Web

- **F4 refreshDetail 健壮性**(P1):包 try/catch;`e.status===404` 时 `closeDetail()`;其余错误保留旧渲染 + `console.warn`;加单飞守卫(一次只允许一个在途),消除轮询/翻页并发时的页序错位。
- **F5 #9 设置跨端陈旧**(Q5 方案):`/api/settings` 挂进现有 5s 轮询;检测到变化更新 `appSettings` 与表单默认值;用户正在编辑的输入框不覆盖。
- **F6 settings 回退漂移**(升级):`settingsLocked` 对缺段/缺字段的回退完整对齐 `DefaultSettings()`(`Enabled:true`/`IntervalSec:300` 补齐,消除旧配置回退 false/0);锁定测试:无 settings 段的旧 config → `GET /api/settings` 返回默认值。
- **F7 重复提交锁**(P2):保存/克隆按钮进入即 `disabled`、`finally` 恢复,双击只建一个对象。
- **F8 服务端小项**:`report_dir` trim 前移(校验与落盘同值,消除校验过程 `MkdirAll` 创建带空格垃圾目录的副作用);`GET /api/report` 先渲染进 `bytes.Buffer` 成功再写头(失败时干净的 500 + 统一中文错误体,不再损坏已提交的 200 响应);`errors_by_kind` 与 `items` 空时序列化 `[]` 而非 `null`(与注释对齐);写接口 JSON 体加 `http.MaxBytesReader`(1MB)。
- **F9 web 小项**:`f-timeout`/`s-timeout` 两处 `Math.round`(小数不再触发误导性 400);五处枚举串回退值包 `esc()`(与 `renderOverview` 口径一致);设置对话框双击 `dlg.open` 守卫;`validateProvider` 检查顺序对齐 Go 侧(首错消息一致);死代码清理(`windowMs`/`overviewTimer`/`renderResults` 未用形参);详情头 `overflow-wrap:anywhere`。

### R3 TUI

- **F10 /page 命令修分页断链**(P1,T1 方案):注册 `/page next|prev`(枚举参数,`/window` 同款交互),注册表 15 条;README 命令表、REQUIREMENTS §4.3、`CommandHintText`、详情页提示四处联动修正;`overview.go` 回退路径补 `resetPaging`(分页可达后 `pageCursor` 跨对象串扰才真实暴露)。
- **F11 菜单跟随滚动**(P2 升级):`drawMenu()` 在 `SetText` 后按 `menuSel` 调 `ScrollTo`,高亮恒在 3 行视窗内;输 `/` 后按 ↓ 走到 `/help` 全程可见。
- **F12 三项行为陷阱**:命令阶段 Enter 在多候选时不执行(要求完全匹配或唯一前缀命中),消除 `/`+Enter=注册表首条 `/quit` 的误触退出;删除失败走取消路径关模态 + 状态栏红字如实反馈(对象已从引擎移除、存储删除失败);`/edit` 目标已删时关闭表单并闪"监测对象已不存在"(与 `exportCurrent` 同款处理),不再静默降级为新建。
- **F13 回归与打磨**:Tab 轮转不再抹掉状态栏闪现(闪现带时间戳,N 秒内 `renderStatus` 保留——9319822 引入的回归);`openBrowser` 失败的 `fmt.Printf` 删除(URL 已进状态栏,直写 raw 终端花屏);过时文档清理(README/REQUIREMENTS 的列表 j/k 死键承诺删除、概览空态"阶段 4b"过时文案改"/new 新建"引导)。

### R4 契约锁定(Q7 方案:测试强制同步,零构建步骤)

- **F14 JS 格式化收敛+锁定**:app.js 格式化函数收敛为单一模块;Go 契约测试经 `web.FS()` 读 app.js 源码,对 `fmtPct/fmtMs/fmtTPS/fmtTPOT/fmtFlip` 以黄金值断言双端一致(复用状态中文表 Go/js 双表锁定的既有先例);JS `fmtTPOT` 补 `<=0` 守卫对齐 Go;Go 侧 `Fmt*` 家族补 `status.go` 同款"双端同步"契约注释。
- **F15 默认提示词统一**:web `defaultPrompt()` 与 `view.DefaultPrompt` 统一文案(当前 web 建的对象用"请回复: pong",与 TUI/API 分叉,跨对象吞吐指标不可比);纳入契约锁定测试;两入口创建的对象探测请求体一致。

## Out of Scope(8 项归档,记录于任务 journal 供后续立项)

- web 5 秒全量重渲染 diff(下拉打断/选区丢失)。
- TUI `exportReport` 同步渲染冻结 UI(后台化小重构)。
- TUI `queueDraw` 关停竞窗阻塞(危害极低窄窗口)。
- 后端 Update/Remove 不等在途手动探测(seq/started_at 倒置致翻页重复一行,低频)。
- 后端超长 JSONL 行(>1MB)使 scanner 放弃后续行(ReadBytes 续读改造)。
- 后端 `DeleteProvider` os.Remove 失败的半完成状态语义改进。
- 后端无换行流的读缓冲内存上限。
- `/api/providers` 每对象一次全量快照的性能放大(轻量徽章端点或世代缓存)。

## Acceptance Criteria

- [x] A1 `go vet ./...` 与 `go test ./... -race` 全绿(含既有测试)。
- [x] A2 留存:单测锁定 slack 行为——追加至 `maxKeep+1` 不触发重写;超过 `maxKeep+slack` 触发一次重写裁回 `maxKeep`;重写与追加的临界区语义不变。(TestTrimSlackHysteresis)
- [x] A3 SSE:单测锁定 `data:␠␠`(两空格)与 `x-custom: v` 行不产生 `protocol_error` 且探测正常完成;`[DONE]` 与既有十态分类回归绿。(TestSSEToleratesKeepAliveAndUnknownFields)
- [x] A4 settings:旧 config 无 settings 段 → `GET /api/settings` 返回 `enabled:true`/`interval_sec:300`;`interval_sec:-5` 被清洗为默认(单测)。(TestSettingsAbsentSectionAndPartialDefaults)
- [ ] A5 web 手测:浏览器 A 开详情、B 端删除对象 → ≤5s 详情自动关闭、无 console 报错;双击保存/克隆只建一个对象;timeout 输入 60.5 可正常保存。
- [ ] A6 #9 手测:TUI 改 `streak_alert` → 网页 ≤10s(两轮轮询)连败徽章阈值更新;用户正在编辑的表单字段不被覆盖。
- [ ] A7 TUI 手测:`/page next` 可见第 21-40 行、`/page prev` 回退;命令表(15 条)与提示文案一致;输 `/` 按 ↓ 至 `/help` 高亮全程可见;`/`+Enter 不退出。
- [ ] A8 TUI:删除失败路径关模态+红字反馈(单测矩阵已过);`/edit` 已删对象闪提示不新建(开 /web 面板手测);`/report` 后 5s 内按 Tab 路径提示仍在(手测)。
- [x] A9 契约:格式化函数与默认提示词的双端锁定测试在位且绿;app.js 无缺守卫的 `fmtTPOT`;`"ok"` 比较字面量全库仅契约常量 `store.StatusOK`。(fmt_sync_test.go + 5 处字面量归常量)
- [x] A10 trellis-check 通过;**重建根目录 `./llm-monitor`**(2026-10-16)后真机冒烟 A5-A8 待用户执行(Session 8 教训:冒烟前必须重建二进制)。
- [x] A11 spec 沉淀:命令注册表 15 条(/page)、SSE 容错口径、格式化+提示词双端锁定契约、留存滞后、GetScrollOffset(row,column) 陷阱、MaxBytesReader/report_dir trim 写入 go-conventions.md。

## 实施记录(2026-10-16)

四批全部落地并提交:cee65b7(R1 后端)、ed2dfef(R2 服务端+web)、b0df70c(R3 TUI)、bff7439(R4 契约锁定)+ spec 沉淀提交。**范围外新发现 F16**(报告未覆盖,实现期发现):`redrawLocked` 把 `GetScrollOffset()` 的 column 当 row 用,详情滚动位置每 5 秒被静默重置到顶部——已修并注释。

### 归档项(Out of Scope,共 8 项,单独立项候选)
见上文 Out of Scope;其中后端 4 项与 `/api/providers` 快照性能放大适合合并为一个"store 性能/健壮性"后续任务,web 重渲染 diff 与 exportReport 后台化为前端体验任务。

## Notes

- 实施按 R1→R4 四批提交(后端 → 服务端+web → TUI → 契约锁定),沿用 10-12-review-fixpack 的分批节奏。
- 四份审查报告原文(含每项 file:line 证据)存于本次会话上下文,实现时逐项对照。
