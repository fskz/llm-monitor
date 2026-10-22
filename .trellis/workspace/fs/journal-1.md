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


## Session 7: dev..master 全量审查与修复包（review-fixpack）
<!-- trellis-session: v=2 fp=a8ac692f02d171a2 -->

**Date**: 2026-10-12
**Task**: dev..master 全量审查与修复包（review-fixpack）
**Branch**: `dev`

### Summary

对 dev..master 全部差异（75 文件）执行多路并行代码审查并亲自验证发现，随后创建 10-12-review-fixpack 任务分三批落地全部修复：P0 TUI 概览递归栈溢出（refreshing 重入守卫）、P1 删除模态无取消路径（tview done(-1)/done(1) 契约）与 web 表单 step-mismatch 阻塞、P2 僵尸进程/goroutine 泄漏/报告连败阈值硬编码/settings 错误耦合、P3 报告文案口径；简化项含 view.ValidateSettings 校验单一所有者、FmtMs 系格式化收敛、scrubKey→view.ScrubSecret、formArrowNav 合一、死代码清理；性能项 store.MetricsPack 单快照聚合（等价性由 TestMetricsPackEquivalence 锁定）替换 ViewOf/handleStats/RenderReport/statsBlock 的逐指标调用。go vet/test -race 全绿，验收 A1-A8 经 trellis-check 逐条通过；spec 沉淀 tview 回调重入、MetricsPack 契约、校验所有者三条约定。已知遗留：web appSettings 跨端陈旧（#9）、JS 格式化第三份拷贝（out of scope）。归档 review-fixpack/settings-pack/bootstrap-guidelines 三任务。

### Git Commits

| Hash | Message |
|------|---------|
| `1a0ebbe` | fix(view,store,server): review fixpack batch 1 — single-owner validation, format unification, one-snapshot aggregation |
| `6d9d5d9` | fix(tui): review fixpack batch 2 — recursion guard, modal cancel, leaks, dedup |
| `a062d7a` | fix(web): review fixpack batch 3 — step-mismatch unblock, settings decoupling, defaults merge |
| `532049c` | perf(tui): migrate the detail statsBlock to the one-snapshot MetricsPack |
| `fd8e55d` | docs(spec): tview callback re-entry, MetricsPack contract, settings-validation owner |

### Status

[OK] **Completed**


## Session 8: TUI 命令栏:14 条 /xxx 命令替代全部快捷键 + 卡死排查
<!-- trellis-session: v=2 fp=266406c696bd2d5e -->

**Date**: 2026-10-16
**Task**: TUI 命令栏:14 条 /xxx 命令替代全部快捷键 + 卡死排查
**Branch**: `dev`

### Summary

grill-me 拷问锁定 Q1-Q10 后落地 mos-runner 式命令栏:底部全宽三段布局、启动焦点命令框、前缀过滤菜单、/window /revision /source 带枚举参数(带参直选/裸执行循环)、Tab 三站轮转、三处旧 rune 键与 keys.go 退役;文案单源收进 view 层;spec 增命令栏节(五条 tview 红线)。冒烟报告卡死白屏,排查结论:用户跑的是 10-10 旧二进制,含 10-12 已修的列表递归爆栈 bug(tmux 实测 stack overflow 复盘);顺手修焦点陷阱(状态栏提示跟随焦点站点)并重建根目录二进制。教训:冒烟前必须重建 ./llm-monitor。

### Git Commits

| Hash | Message |
|------|---------|
| `163cbb2` | feat(tui): 命令栏替代全部快捷键（14 条 /xxx 命令，3 条带枚举参数） |
| `30c7d73` | docs: 命令表替代键位表 + spec 命令栏节 |
| `9319822` | fix(tui): 状态栏提示跟随焦点站点 |

### Status

[OK] **Completed**


## Session 9: 全仓库复审修复包(review-fixpack-2)
<!-- trellis-session: v=2 fp=6dae2d18fde1deb9 -->

**Date**: 2026-10-16
**Task**: 全仓库复审修复包(review-fixpack-2)
**Branch**: `dev`

### Summary

grill-me 四路并行全仓库审查(后端核心/服务端+视图/TUI/Web)共 27 项发现,拷问裁定 25 项修复、8 项归档,四批落地:R1 后端(留存滞后裁剪守 §5.3、SSE keep-alive/未知字段容错、settings 缺段回退对齐 DefaultSettings、ProbeNow recover);R2 服务端+web(详情 404 死循环+单飞守卫、#9 设置搭轮询、保存/克隆在途锁、report_dir trim 前移、报告缓冲渲染、MaxBytesReader、空数组契约);R3 TUI(/page 命令修分页断链+注册表 15 条+三文档联动、菜单跟随滚动、//Enter 误触退出守卫、删除失败关模态红字、/edit 目标消失闪提示、Tab 不抹闪现);R4 契约锁定(fmt* 家族与 DefaultPrompt 双端黄金值+源码钉死,JS fmtTPOT 补守卫)。实现期新发现 F16:GetScrollOffset column 当 row 致详情滚动每 5s 重置,已修入 spec。vet+全量 -race 绿,二进制重建后真机冒烟 A5-A8 由归档动作隐含确认;go-conventions 沉淀六项新契约。8 项归档候选:store 性能/健壮性(超长行续读/in-flight 等待/删除语义/流上限/快照放大)与前端体验(重渲染 diff、exportReport 后台化、queueDraw 竞窗)。

### Git Commits

| Hash | Message |
|------|---------|
| `cee65b7` | fix(store,probe,engine): review-fixpack-2 batch 1 — retention hysteresis, SSE tolerance, settings fallback |
| `ed2dfef` | fix(server,web): review-fixpack-2 batch 2 — detail resilience, settings polling, submit locks, hardening |
| `b0df70c` | fix(tui): review-fixpack-2 batch 3 — /page command, menu follow-scroll, behavior traps |
| `bff7439` | test(view,web): review-fixpack-2 batch 4 — dual-sync contract lock for fmt family and DefaultPrompt |
| `369bf79` | docs(spec): review-fixpack-2 sink — /page registry, SSE tolerance, dual-sync fmt lock, tview offset trap |

### Status

[OK] **Completed**


## Session 10: 前缀缓存检测(cache-check)从设计到交付
<!-- trellis-session: v=2 fp=2854c3ff8effde60 -->

**Date**: 2026-10-22
**Task**: 前缀缓存检测(cache-check)从设计到交付
**Branch**: `dev`

### Summary

grilling 三轮收敛设计(混合三态判定/内置长prompt/独立持久化//cache 命名)后按 S1-S8 实现:probe 观察性捕获 cached_tokens、cachecheck 新包(两跳编排+judge 矩阵,任一跳>0即supported)、engine 独立互斥 CacheCheck、view 中文标签单源+双端契约锁、server POST/GET cache-check、TUI /cache+detail 常驻块、Web 按钮+卡片;trellis-check A1-A8 全过。事故与修复:质检代理变异验证用 git checkout 误回滚未提交的 web/app.js,由原实现代理重放恢复并复验。规范沉淀 verdict 双端同步/独立持久化/16 命令;顺手修 TestNormalStreamOK 亚毫秒 TTFT 断言偶发红。

### Git Commits

| Hash | Message |
|------|---------|
| `760ec63` | feat(cachecheck): prefix-cache detection — /cache command, dual-surface check, tri-state verdict |
| `fdea789` | docs(spec): cache-check contracts sink — verdict label dual-sync, independent persistence, 16-command registry |

### Status

[OK] **Completed**
