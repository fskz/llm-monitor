# Journal - fs (Part 1)

> AI development session journal
> Started: 2026-10-08

---


## 2026-10-08 · task 10-08-mvp-llm-monitor — MVP 实现完成
- 全量实现 REQUIREMENTS.md v0.2 MVP：probe(17测)/store(13测)/engine(10测)/server(12测)/web/Makefile。
- 子代理并行分发 S2/S3、S4/S5；主会话完成 S1/S6/S7 集成与冒烟（mock 端到端：启动即探测、手动 409、Origin 403、重启一致、平滑退出）。
- trellis-check 修复 8 项（关键：probe deadlinePassed 判定 bug、engine select 双就绪幽灵 cancelled 记录、main log.Fatal 跳过优雅退出、ProbeNow seq=0 契约、web XSS/失败原因展示）。
- 沉淀 .trellis/spec/backend/go-conventions.md（状态字符串四处同步契约、JSONL 纪律、调度 select gotcha）。


## Session 1: 实现 LLM 接口可用性监测工具完整 MVP
<!-- trellis-session: v=2 fp=b81b8d17432e6011 -->

**Date**: 2026-10-08
**Task**: 实现 LLM 接口可用性监测工具完整 MVP
**Branch**: `dev`

### Summary

按 docs/REQUIREMENTS.md v0.2 实现 MVP：probe 流式探测判定（双期限+10 状态分类）、store JSONL 存储统计、engine 独立调度、server REST API（六态状态/Key 掩码/同源防护）、内嵌 Web 面板与 6 平台交叉构建。52 项测试全绿（-race）；trellis-check 修复 8 项缺陷（关键：首内容期限解除判定 bug、select 双就绪幽灵 cancelled 记录）；沉淀 spec/backend/go-conventions.md。验收 A1 全过、A2 除真机 Windows 双击手测外全过（产物 dist/）。

### Git Commits

| Hash | Message |
|------|---------|
| `1da8ba5` | feat: implement LLM availability monitor MVP (engine, store, API, panel) |
| `cac3ede` | docs: add requirements doc v0.2 |
| `d16acb5` | chore(trellis): task artifacts and Go conventions spec |

### Status

[OK] **Completed**

## 2026-10-09 · task 10-09-usage-throughput-metrics — usage 吞吐指标完成
- 每对象 include_usage 开关：stream_options 按需携带；usage 捕获独立于分类（usage-after-[DONE] 不消费）；Result 增 tokens 字段（null 语义）。
- 吞吐口径单一实现（store decodeTPS/prefillTPS + ResultTPS）：decode 剔除 completion<2/零时长，prefill 标注近似含排队；手动响应与统计共用。
- check 修复 7 项（flaky engine 测试、REQUIREMENTS §9.2 与新字段矛盾、误导注释、series 覆盖缺口、A6 旧数据直接覆盖）；冒烟 decode=57.5/prefill=238.8 与公式一致。
- 教训：pkill -f 模式会匹配自身命令行（exit 144）——冒烟脚本改用 PID 管理（scripts/smoke_usage.py）。


## Session 2: 探测指标增加 usage 精确吞吐统计
<!-- trellis-session: v=2 fp=89f82cf226d59b0a -->

**Date**: 2026-10-09
**Task**: 探测指标增加 usage 精确吞吐统计
**Branch**: `dev`

### Summary

每对象 include_usage 开关（默认关，请求体字节级不变）：stream_options 按需携带，usage 捕获独立于分类判定；Result 增 prompt/completion_tokens（null 语义，旧数据兼容）；decode/prefill 吞吐口径单一实现于 store（decode 剔除 completion<2 与零时长样本，prefill 标注含排队近似，独立分母）；统计/趋势/手动响应/面板全链路展示。check 修复 7 项；冒烟 decode=57.5/prefill=238.8 与公式一致；全量 -race 绿。REQUIREMENTS.md 受控修订（§1.4/§3.1/§3.2/§9/§12.1-9/决策 #11）。

### Git Commits

| Hash | Message |
|------|---------|
| `5298015` | feat: add opt-in usage-based throughput metrics (decode/prefill tps) |
| `1278546` | docs: revise requirements for opt-in usage throughput metrics |
| `a0fe4ec` | chore(trellis): task artifacts, spec and journal for usage metrics |

### Status

[OK] **Completed**

## 2026-10-09 · task 10-09-export-html-report — HTML 报告导出完成
- GET /api/report（attachment，参数同 stats+source 缺省 all）：服务端 html/template 渲染单对象自包含报告（徽章/统计/吞吐/双 SVG 趋势/明细 ≤200）。
- 唯一 template.HTML 为 SVG 数字几何；Key 三层防线（掩码/probe 脱敏/渲染时 scrubSecret）；中文对象名 slug 空回退 provider-<id>。
- check 修复 3 项：延迟图零样本占位、Token 列 null 误显 0、文件名补 PRD 前缀与秒级时间戳；状态中文表 Go/js 双表同步入规范。
- 冒烟：7 项断言全过（无 Key/无外链/svg/统计卡/近似标注/明细/徽章）。


## Session 3: 导出 HTML 监控报告
<!-- trellis-session: v=2 fp=788bafac1f4376e2 -->

**Date**: 2026-10-09
**Task**: 导出 HTML 监控报告
**Branch**: `dev`

### Summary

GET /api/report 导出单对象自包含 HTML 报告（服务端 html/template 渲染：徽章/三率/吞吐/双 SVG 趋势/明细 ≤200 含失败原因，attachment 下载，source 缺省 all）。Key 三层防线；唯一 template.HTML 为数字几何 SVG；状态中文表 Go/js 双表同步入规范。check 修复 3 项（空态占位/null≠0/文件名补前缀）；冒烟 7 项断言全过；全量 -race 绿。REQUIREMENTS §4.5/§10/§13/决策 #12 修订。

### Git Commits

| Hash | Message |
|------|---------|
| `9a1a94a` | feat: export single-provider self-contained HTML monitoring report |
| `195c231` | docs: revise requirements for HTML report export |
| `bf9513e` | chore(trellis): task artifacts, spec and journal for report export |

### Status

[OK] **Completed**


## Session 4: TUI 默认模式落地:零常驻端口改造全流程
<!-- trellis-session: v=2 fp=443df22d00cdb27d -->

**Date**: 2026-10-10
**Task**: TUI 默认模式落地:零常驻端口改造全流程
**Branch**: `dev`

### Summary

grill-me 拷问定架构(tview TUI 默认/零监听/web 按需拉起/单进程单实例/视图下沉共享包)后分阶段实施:internal/view 下沉视图组装与报告纯函数化(含 web/app.js 状态表契约测试)、main 重构为零监听启动、internal/tui 双栏全功能界面(列表/详情/sparkline/分页筛选/表单/手动探测/报告导出/按需 web)、REQUIREMENTS 与 go-conventions 同步修订、新增 README。过程中修复 .gitignore 吞掉 cmd/llm-monitor 源码目录的问题,冒烟反馈修复表单上下键导航与模态中全局热键吞输入两个交互 bug。统计样本跟随来源筛选的需求已确认,下一任务处理。

### Git Commits

| Hash | Message |
|------|---------|
| `b103045` | refactor(view): extract shared view assembly layer from server |
| `588a499` | feat(tui): make the terminal UI the default front end with zero listening ports |
| `a3c7b39` | docs: revise requirements and Go conventions for the TUI default mode |
| `0cd0410` | chore(trellis): task artifacts for 10-09-tui-default |
| `270ed60` | fix(tui): navigate form fields with Up/Down keys |
| `15bcd71` | docs: add README with configuration and usage guide |
| `4c96238` | fix(tui): stop global hotkeys from swallowing form input |

### Status

[OK] **Completed**


## Session 5: 统计来源筛选 + TPOT + 克隆 + 一批冒烟修复
<!-- trellis-session: v=2 fp=c0f37b773bf57be8 -->

**Date**: 2026-10-10
**Task**: 统计来源筛选 + TPOT + 克隆 + 一批冒烟修复
**Branch**: `dev`

### Summary

stats-by-source 任务名下交付四项:①统计卡/趋势图跟随来源筛选(store 四函数加 source 参数,/api/stats|series 接 source 缺省 all,监测徽章保持定时口径);②TPOT 指标四端展示层换算(1000/decode_tps,同证据双读法);③克隆功能(服务端 POST /api/providers/{id}/clone 复制含 key 不经响应体,web 克隆后自动聚焦模型字段);④概览卡报错长文本挤压按钮的布局修复(卡片列 flex,按钮钉底)。用户真实终端冒烟全部通过。grill-me 后续已定两任务:指标包(P50/P95+连续异常+错误分布+抖动)与全局设置包(默认值预填+失败阈值提示+报告路径)。

### Git Commits

| Hash | Message |
|------|---------|
| `c9872e0` | feat(stats): make detail statistics follow the source filter |
| `503e428` | feat(metrics): add TPOT (time per output token) across all front ends |
| `044bcd4` | feat(web): clone a provider for same-channel model variants |
| `112d4f7` | fix(web): pin overview card action buttons below the failure detail |

### Status

[OK] **Completed**


## Session 6: 指标增强包:P50/P95+连续异常+错误分布+抖动
<!-- trellis-session: v=2 fp=336b8ad443c63a4c -->

**Date**: 2026-10-10
**Task**: 指标增强包:P50/P95+连续异常+错误分布+抖动
**Branch**: `dev`

### Summary

metrics-pack 任务交付四项聚合层指标:store/quantiles.go 四个 Compute* 函数(最近邻秩分位数/连续计数/错误分布/翻转率,数学被 A1-A4 单测锁定),/api/stats 扩展字段+ProviderView.streak_fail,web 统计卡与概览连败角标,TUI 统计块,报告新卡;全部跟随来源筛选口径,P99 明确不做。指标包冒烟由用户开任务2的动作隐含确认。

### Git Commits

| Hash | Message |
|------|---------|
| `528ccb1` | feat(metrics): percentile, streak, error-breakdown and flip-rate pack |

### Status

[OK] **Completed**
