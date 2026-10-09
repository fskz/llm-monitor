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
