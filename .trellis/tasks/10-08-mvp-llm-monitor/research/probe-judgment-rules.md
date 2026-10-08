# 探测判定规则研究笔记（源自 REQUIREMENTS.md §3，实现必读）

本笔记把需求文档中最容易实现出错的判定规则浓缩为可执行口径，供 trellis-implement / trellis-check 子代理直接引用。与 REQUIREMENTS.md 冲突时以需求文档为准。

## 首内容判据（TTFT）

- 首内容 = 首个 SSE 事件中 `choices[0].delta.content` 的**非空字符串**（len > 0）。
- 以下均**不算**首内容：角色事件（delta 仅有 `role` 字段）、空 delta、心跳/注释行（`:` 开头）、usage 事件、`[DONE]`、finish_reason 标记、reasoning 字段、tool_calls 字段。
- TTFT = 请求发起（单调时钟）→ 首个非空 content 到达。
- 全部耗时（TTFT、总耗时）用单调时钟（Go `time.Since`），不用墙钟；started_at/finished_at 用 UTC Unix ms 墙钟。

## 成功与结束判据

成功（status=ok）必须同时满足：
1. HTTP 2xx；
2. 在两个期限内收到非空有效文本；
3. 无流内错误 / 协议错误；
4. 满足正常结束条件（二者之一）：
   a. 收到 `[DONE]` 行；或
   b. 已收到非空 `finish_reason` 后正常 EOF。

其他分类：
- 正常结束但无有效文本 → `empty`（错误类）。
- EOF 但无任何结束证据（既无 [DONE] 也无非空 finish_reason）→ `aborted`，**即使已收到部分文本**。
- `finish_reason=length`（达到 max_tokens）→ 成功。
- HTTP 200 响应中的错误事件（如 `data: {"error": ...}`）→ `stream_error`。
- 无法解析的事件行 → `protocol_error`。

## 超时分类（§3.3 注、§12.1-3）

- 两个期限从同一请求开始时刻起算，先到期者决定超时类型。
- **同时到期且仍无首内容 → 固定 timeout_ttft**。
- 探测期限导致的底层错误（超时后连接被砍产生的 read error 等）优先归为对应超时，不记 conn_error。实现要点：超时判定不依赖错误回调，而是读循环中每步检查 `ctx.Err()` 或 deadline；分类时先看期限是否已到。
- **输出中途总超时（已有部分文本、无结束证据、总期限到）→ `timeout_total`**，不是 aborted（aborted 是对端异常断开；超时是我们主动终止）。
- TTFT 已测得后发生错误或总超时：保留 ttft_ms 值，最终结果仍为失败。
- 实现建议：runtime deadline = min(ttft_deadline, total_deadline)；收到首内容后切换为 total_deadline。到期时：若从未收到首内容 → timeout_ttft（包含同时到期情形）；否则 → timeout_total。

## 错误分类决策树

HTTP 响应未建立时（连接/请求阶段）：
```
if 期限已到              → timeout_ttft（无首内容时）
elif HTTP non-2xx        → http_error（记录状态码 + 脱敏 body 片段）
elif DNS/连接/TLS 失败   → conn_error
```
HTTP 200 已建立、流读取阶段：
```
if [DONE] 或 (非空 finish_reason 后 EOF)，且有非空文本 → ok
elif 期限到期            → timeout_total（已有文本）/ timeout_ttft（无文本）
elif 流内 error 事件     → stream_error
elif 无法解析的事件行    → protocol_error
elif EOF 无结束证据      → aborted
elif 正常结束但无文本    → empty
```

## SSE 解析规则

- **行解析必须在读缓冲上按 `\n` 扫描**，不能按 HTTP chunk 输入切片（chunk 边界与行边界不对齐）。
- `data: ` 前缀（一个空格）与 `data:`（无空格）均接受。
- 注释行（`:` 开头）与纯空白行跳过。
- `data: [DONE]` 为结束标记。
- 每行独立解析为 JSON；不合并多行 data（OpenAI 实践每事件一行 data，MVP 按行解析）。
- JSON 解析失败的 data 行 → protocol_error（§12.1-4）。
- `choices` 缺失或为空的事件（如 usage 事件）→ 跳过，不判错（接口可能主动附带 usage；我们不请求 include_usage）。
- `choices[0].delta.content` 空串/缺失 → role/空 delta 事件，跳过（不触发首内容、不判错）。
- `event:` 行：忽略 event 名，按 data 内容判定。
- 流内错误事件判据：JSON 顶层含非空 `error` 对象 → stream_error。

## 统计口径

- 分母 = 已完成、实际发起、非 cancelled 的**定时**探测；手动（manual）不进默认统计。
- 成功 + 超时 + 错误 = 100%（三类互斥穷尽）；超时率内部区分 timeout_ttft / timeout_total 子类。
- 无样本 → 比例返回 null，面板显示"暂无样本"，绝不 0%/100%。
- 时间窗按 started_at 筛选：1h / 24h / 7d。
- 运行中、跳过的调度、停用期间、未运行期间不进分母；不补样本、不插值。
- 趋势桶：成功比例 + 成功请求耗时 + 每桶样本数；空桶返回 null。
- 失败请求的耗时（total_ms）不混入成功响应耗时统计。
