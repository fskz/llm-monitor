# 技术设计 — usage 精确吞吐指标

> 需求：本任务 prd.md；判定口径基线 docs/REQUIREMENTS.md §3（本任务修订其中 include_usage 条款）。

## 1. 数据流（新增部分加粗）

```
Provider.include_usage ──► **probe.Target.IncludeUsage**
    └─ true 时请求体 + stream_options:{include_usage:true}
SSE usage 事件 ──► **streamEvent.Usage 捕获** ──► **Outcome.PromptTokens/CompletionTokens**
    └─ engine 复制 ──► **Result.prompt_tokens/completion_tokens**（JSONL 落盘）
统计：ok 样本 + usage 完备 ──► **decode_tps/prefill_tps** ──► Stats/Series/API/面板
```

依赖方向不变：probe 不依赖 store/engine；store 不解析 usage（只存数字）；server 只做映射。

## 2. probe 包

```go
// Target 增：
IncludeUsage bool

// chatRequest 增（probe.go:158 附近）：
StreamOptions *streamOptions `json:"stream_options,omitempty"`
type streamOptions struct{ IncludeUsage bool `json:"include_usage"` }

// streamEvent 增（sse.go:14）：
Usage *usageInfo `json:"usage"`
type usageInfo struct {
    PromptTokens     *int `json:"prompt_tokens"`
    CompletionTokens *int `json:"completion_tokens"`
}

// Outcome 增：
PromptTokens, CompletionTokens *int

// run 状态增：捕获到 usage 时记录（最后到达者胜——多事件携带 usage 时取最后一个，
// OpenAI 语义只有流末一个；不参与任何分类分支）。
```

边界与守卫：
- **usage-after-[DONE]**：读循环在 [DONE] 即返回（processLine 语义不变）；标准实现 usage 在 [DONE] 前到达能捕获；非标准"先 [DONE] 后 usage"捕获不到 → nil。不改终止语义（判定规则优先级高于指标采集）。
- usage 值为 0：照常捕获（0 也是合法观测值）；负数/超大方差不管（服务端数据原样记录）。
- 捕获点在 handleData 的 choice-less 跳过分支之前统一做：任何成功解析的事件若带 usage 字段即记录，再走原有 choices 判定。

## 3. store 包

- Provider 增 `IncludeUsage bool json:"include_usage"`（旧 config 缺省 false）。
- Result 增：
```go
PromptTokens     *int `json:"prompt_tokens"`
CompletionTokens *int `json:"completion_tokens"`
```
  （与 ttft_ms 一致的 null 语义，不用 omitempty——null 明示"未观测"而非"字段不存在"。）
- Stats/SeriesBucket 增 `AvgDecodeTPS, AvgPrefillTPS *float64`（json: avg_decode_tps/avg_prefill_tps）。
- AvgOnOK 扩展（或新增 ThroughputOnOK）：在现有 ok 样本循环上，对 usage 完备样本累加：
  - decode：`completion_tokens>=1 && total_ms>ttft_ms` → `(completion_tokens-1)/((total-ttft)/1000)`；否则跳过该样本（计入 prefill 分母与否独立判断）。
  - prefill：`prompt_tokens>0 && ttft_ms>0` → `prompt_tokens/(ttft_ms/1000)`。
  - 两指标各自独立计数分母（一个样本可能只有其一可算）。
- Series 桶内同规则聚合。

## 4. engine 包

- runProbe（engine.go:319）构造 Target 时传 `IncludeUsage: p.IncludeUsage`；Outcome 的 tokens 复制进 Result（cancelled/失败样本照抄 nil 或捕获值——失败样本 usage 不可得，天然 nil）。

## 5. server 包

- providerPayload 增 include_usage 布尔（默认 false）；视图概览返回。
- /api/stats、/api/series 映射两个新均值字段。
- 手动探测响应：store.Result 直接 Marshal 已含 prompt_tokens/completion_tokens；另附计算出的 `decode_tps`/`prefill_tps`（可算时，null 否则）。

## 6. web 前端

- 配置表单（index.html + app.js readProviderForm/openProviderDialog）：复选框"吞吐指标（请求 usage）"。
- 详情统计行：平均 decode 吞吐（tok/s）、平均 prefill 吞吐（tok/s，标注"近似·含排队"）；fmt 函数 `fmtTPS`（null→"—"，保留 1 位小数）。
- 手动测试结果表：prompt/completion tokens + decode_tps/prefill_tps（有值时）。

## 7. REQUIREMENTS.md 修订点

| 位置 | 修订 |
|------|------|
| §3.1 表 | 追加两行：decode 吞吐（ok 样本，tok/s，可选）；prefill 吞吐（近似下界，可选） |
| §3.2:72 | 改为"默认不请求 stream_options.include_usage；对象级开关开启时请求，token 数仅取服务端 usage 上报，不做估算；usage 缺失不影响判定" |
| §9.1 表 | 增 include_usage 字段行 |
| §9.2 表 | 增 prompt_tokens/completion_tokens 行（null=未观测） |
| §12.1 | 追加验收 9：开关/捕获/计算/兼容（映射本任务 A2-A6） |
| §14 | 追加决策 #11 |

## 8. 权衡记录

| 决策 | 备选 | 取舍 |
|------|------|------|
| usage-after-[DONE] 不捕获 | 读循环越过 [DONE] 续读 | 终止语义是判定基线（§3.2），指标采集不得改变分类行为；标准实现 usage 在 [DONE] 前 |
| 两吞吐独立分母 | 合并分母 | 一样本可能只有其一可算（如 completion=1 只进 prefill） |
| null 语义不加 omitempty | omitempty | 与 ttft_ms 一致；"未观测"显式可见 |
| 失败样本不存 tokens（自然 nil） | 强制捕获 | 失败时服务端几乎不返回 usage；不增加解析负担 |

## 9. 兼容 / 回滚

- 全部新字段向后兼容（旧数据缺省零值/nil）；开关默认关 → 默认行为逐字节等同现状。
- 回滚：revert 提交即可，数据文件无需迁移（新字段对旧代码是未知键，忽略）。

## 10. 测试设计（映射 A1-A8）

- probe：A1（关：请求体无 stream_options）、A2（开：有 + usage 捕获）、A3（无 usage→nil）、usage-after-DONE 边界（nil 且仍 ok）、usage=0 捕获。
- store：Result 新字段序列化 null、旧 JSONL 加载、A4 指标计算（含除零/completion=1 跳过）、Series 新桶字段。
- engine：Target 传递（mock 断言请求体）。
- server：stats/series/probe 响应含新字段、payload 往返。
- web：手动检查项（A7），无自动化。
- 回归：全量 -race（A1 保证既有测试零改动通过）。
