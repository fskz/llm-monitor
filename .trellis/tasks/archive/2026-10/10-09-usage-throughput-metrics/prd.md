# 探测指标增加 usage 精确吞吐统计

> 来源：用户请求"增加 prefill/decode 监控指标"（2026-10-09），方案已确认：仅用服务端 usage 上报的精确 token 数计算吞吐，不做字符/delta 估算。本文档同时是对 REQUIREMENTS.md 基线的受控修订（原 §1.4/决策 #10 将 token/TPS 列为后置）。

## Goal

为每个监测对象提供可选的吞吐观测：开关开启时探测请求附带 `stream_options.include_usage`，用服务端在流末 usage 事件中上报的 `prompt_tokens`/`completion_tokens`，为 ok 样本计算并展示 decode 吞吐与 prefill 吞吐（近似），使"接口可用性优先、响应速度为辅"的定位下能观察输出速度，且不破坏可用性判定的既有口径。

## Background / Confirmed Facts（代码取证）

- 真·prefill/decode 切分是引擎内部概念，OpenAI 兼容协议客户端不可见；TTFT=排队+prefill（上界），decode 可由 usage token 数+TTFT/总耗时精确推导。
- usage 事件当前被有意跳过：internal/probe/sse.go:89-91（choice-less 事件跳过不算错）；streamEvent 结构体注释（sse.go:10-13）明确 usage 字段"不参与判定"。
- 请求体构造：internal/probe/probe.go:158 `chatRequest`（现无 stream_options）。
- 请求入参：internal/probe/probe.go:50 `Target`；engine 构造点 internal/engine/engine.go:319。
- Result 结构：internal/store/store.go:78-94；Stats/AvgOnOK：internal/store/stats.go:14-23/67-71；server 映射 internal/server/stats_api.go:128。
- 前端统计行 web/app.js:207、手动测试结果 :438、配置表单（index.html）。
- REQUIREMENTS.md:72 明文"不主动请求 include_usage"——本任务修订为"默认不请求，每对象可选开启"。

## Requirements

### R1 探测请求（probe）
- Target 新增 `IncludeUsage bool`；true 时请求体加 `"stream_options":{"include_usage":true}`；false 时完全不发送该字段（保持现状，兼容不识别 stream_options 的端点）。
- SSE 解析捕获 usage：`streamEvent` 新增 `usage` 可选字段（prompt_tokens/completion_tokens），任何事件中携带即记录到 Outcome（新字段 `PromptTokens/CompletionTokens *int`）；usage 捕获不影响分类判定（仍是 choice-less 跳过路径）。
- usage 缺失/为 0/字段类型异常时 Outcome 对应字段为 nil，成功判定不变（§3.2 既有口径）。
- usage 事件在 [DONE] 之后到达的极端实现：读循环在 [DONE] 即终止，捕获不到 → nil（接受，写入设计说明）。

### R2 配置（store/server/web）
- Provider 新增 `include_usage bool`（JSON `include_usage`，缺省 false，旧 config.json 兼容）。
- 无新增校验规则（布尔开关，无交叉约束）。
- API：POST/PUT 接受 `include_usage`；GET /api/providers 概览返回该字段。
- 前端配置表单：勾选框"吞吐指标（请求 usage）"，默认不勾。

### R3 结果与统计
- Result 新增 `prompt_tokens`/`completion_tokens`（*int，无值 null；旧 JSONL 记录兼容）。
- 指标定义（仅 status=ok 且 usage 完备的样本；不完备样本不进吞吐均值分母）：
  - `decode_tps = (completion_tokens-1) / (total_ms - ttft_ms) * 1000`；要求 completion_tokens≥1 且 total_ms>ttft_ms（decode 时长为 0 视为无法计算 → 该样本不计入）。
  - `prefill_tps = prompt_tokens / ttft_ms * 1000`；要求 ttft_ms>0；**含排队等待，标注为近似下界**。
- Stats 概览与 Series 桶新增 `avg_decode_tps`/`avg_prefill_tps`（*float64，无样本 null）。
- 手动探测响应包含 token 数与两项吞吐（可计算时）。

### R4 面板
- 详情统计行新增"平均 decode 吞吐 / 平均 prefill 吞吐（ok 样本）"，null 显示"—"；prefill 项附"近似（含排队）"说明。
- 手动测试结果展示 token 数（有 usage 时）。
- 趋势图不扩展（范围控制，后续可加）。

### R5 需求文档同步
- REQUIREMENTS.md：§3.1 指标表、§3.2:72（include_usage 条款）、§9.1/§9.2 字段表、§12 验收补充；决策记录追加 #11（吞吐指标按对象开关，仅 usage 精确计数，不做估算）。

## Acceptance Criteria

- [ ] A1 开关关闭（默认）：请求体无 stream_options（抓包断言）；行为与现状完全一致（全部既有测试不改动即通过）。
- [ ] A2 开关开启：请求体含 stream_options.include_usage=true；流末 usage 事件被捕获，Result.prompt_tokens/completion_tokens 正确落盘。
- [ ] A3 usage 缺失：Outcome 字段 nil、JSONL 序列化为 null、成功判定不变。
- [ ] A4 指标计算：构造 ttft=200ms/total=1200ms/prompt=64/completion=32 的 ok 样本 → decode_tps=(32-1)/1.0=31.0、prefill_tps=64/0.2=320.0（容差 ±5%）；completion_tokens=1 或 total==ttft → 不计入均值。
- [ ] A5 统计与 API：/api/stats 与 /api/series 返回 avg_decode_tps/avg_prefill_tps；无 usage 样本时为 JSON null；手动探测响应含 tokens。
- [ ] A6 旧数据兼容：无新字段的旧 JSONL 与 config.json 加载正常，新字段缺省 false/nil。
- [ ] A7 面板：开关可配置；详情页显示两项吞吐（无值"—"，prefill 带近似标注）；手动结果展示 tokens。
- [ ] A8 全量回归：gofmt/vet/`go test ./... -race` 全绿；REQUIREMENTS.md 修订完成。

## Out of Scope

- 字符/delta 估算 token、ITL 中位间隔（方案讨论中被否决）。
- 引擎真实 prefill/decode 指标（Prometheus 拉取 vLLM/SGLang metrics 属另一类监控）。
- 趋势图扩展、吞吐分布/分位数、导出。

## Technical Notes

- 修改锚点清单见 Background；详细设计（含 usage-after-[DONE] 边界、除零守卫）在 design.md。
- 基线修订原则：默认行为不变，新能力完全 opt-in；这使本任务与原 §1.4"不做 token 估算"决策兼容（我们不估算，只用服务端精确值）。

## Open Questions

无。
