# 实施计划 — usage 精确吞吐指标

> 前置：prd.md（A1-A8）、design.md（锚点与守卫）。改动面小且分层清晰，主会话单代理顺序实现即可（不再并行分发）。

## 步骤（自底向上）

### S1 probe 层
- [ ] Target.IncludeUsage；chatRequest.StreamOptions（omitempty）；streamEvent.Usage 捕获（handleData，不碰分类分支）；Outcome.PromptTokens/CompletionTokens
- 验证：`go test ./internal/probe/ -race -count=1`（新增 5 个用例 + 既有 17 个零改动通过）

### S2 store 层
- [ ] Provider.IncludeUsage；Result.prompt_tokens/completion_tokens；Stats/SeriesBucket 吞吐均值；聚合守卫（独立分母、除零/completion=1 跳过）
- 验证：`go test ./internal/store/ -race -count=1`（新增指标计算与旧数据兼容用例）

### S3 engine 层
- [ ] runProbe 传 IncludeUsage、复制 tokens 进 Result
- 验证：`go test ./internal/engine/ -race -count=1`（mock 断言请求体含 stream_options）

### S4 server 层
- [ ] providerPayload.include_usage；概览视图字段；stats/series 映射；手动探测响应附 decode_tps/prefill_tps
- 验证：`go test ./internal/server/ -race -count=1`

### S5 前端
- [ ] 配置表单复选框；统计行两项吞吐（fmtTPS、null→"—"、prefill 近似标注）；手动结果 tokens 展示
- 验证：`node --check web/app.js` + 手动冒烟（make build + mock）

### S6 文档
- [ ] REQUIREMENTS.md 修订（design §7 表：§3.1/§3.2/§9.1/§9.2/§12.1/§14 决策 #11）

### S7 质量检查（trellis-check）
- [ ] A1-A8 逐条核对；全量 `go test ./... -race` + gofmt/vet；跨层 JSON 契约一致性（Result tag ↔ server 映射 ↔ app.js 字段名）

## 风险 / 回滚

| 风险 | 缓解 |
|------|------|
| 捕获逻辑误碰分类分支 | 捕获点在 choice-less 跳过分支之前、纯记录不改状态机；A1 既有测试零改动作守卫 |
| 端点对 stream_options 返回 400 | 属真实可用性观测（http_error）；文档说明 + 开关默认关 |
| 旧数据兼容 | 字段缺省零值；store 新增用例显式覆盖 |

回滚点：S1-S6 每步一个 commit；数据文件无需迁移。

## 完成定义

prd.md A1-A8 全部通过；默认行为逐字节等同现状（A1）。
