# LLM 接口可用性监测工具完整 MVP

> 需求来源：docs/REQUIREMENTS.md v0.2（可用性优先，评审修订稿）。本 PRD 为其执行摘要；判定规则细节见 research/probe-judgment-rules.md、research/state-and-scheduling.md。冲突时以 REQUIREMENTS.md 为准。

## Goal

实现需求文档定义的完整 MVP（里程碑 M1+M2）：一个 Go 编写的跨平台单文件本地工具，对 OpenAI 兼容接口与模型做周期性单请求流式探测，判定可用性（成功/超时/错误），本地 Web 面板展示当前状态、成功率/超时率/错误率、趋势与失败明细，满足需求文档第 12 节全部验收标准。

## Background / Confirmed Facts

- 仓库为绿地项目：无既有代码、无既有规范约定（.trellis/spec 均为待填模板）。
- 需求文档 v0.2 已评审定稿全部产品决策（第 14 节决策记录 10 项）：技术选型 Go + JSONL + go:embed；明文 Key；端口 10110 起；单请求不重试；MVP 无导出/推送/多协议。
- 目标环境包含 Windows 单机（双击启动），Linux/macOS 同源构建。

## Requirements（按需求文档章节映射）

### R1 探测引擎（§3、§4.2）
- 发送 OpenAI 兼容流式 `chat/completions` 文本请求，Base URL 去尾 `/` 后追加 `/chat/completions`，不自动补 `/v1`。
- 首内容判据：首个 `choices[0].delta.content` 非空字符串；角色/空 delta/心跳/usage/[DONE]/finish_reason/reasoning/tool_calls 均不算。
- 成功 = HTTP 2xx + 期限内收到非空文本 + 无协议/流内错误 + 正常结束（[DONE]，或非空 finish_reason 后正常 EOF）。
- 最终分类 9 种：ok / timeout_ttft / timeout_total / http_error / conn_error / stream_error / protocol_error / empty / aborted；程序退出等主动取消为 cancelled（不进统计）。
- 同时到期且无首内容 → timeout_ttft；输出中途总超时 → timeout_total；期限导致的底层错误归超时不归 conn_error；TTFT 测得后失败仍保留 ttft_ms。
- TTFT/总耗时用单调时钟；started_at/finished_at 用 UTC Unix ms。
- 不自动重试、不做并发压测、不请求 include_usage；错误明细含 HTTP 状态码与脱敏原因，长度受限。

### R2 配置管理（§4.1）
- Web 增删改查 + 启用/停用；字段：name、base_url、api_key、model、prompt、max_tokens、timeout_sec、ttft_timeout_ms、ttft_slow_ms、interval_sec、enabled。
- 校验：`max_tokens>0`；`0<ttft_slow_ms<ttft_timeout_ms≤timeout_sec×1000`；`interval_sec=0 或 ≥60`；base_url 为 http(s)；name 非空。
- 默认值：提示词约 64 字符、max_tokens=128、间隔 300s、ttft 超时 10s、总超时 60s、慢阈值 2s。
- 持久化；base_url/model 变更 → revision+1，新版本统计从零开始，旧版本历史可查；其他字段变更不递增 revision 但取消在途并重调度。
- 修改/停用/删除先取消在途（cancelled 不计故障）；删除需确认，删除配置及历史。

### R3 调度（§7.1）
- 启动/启用后立即探测一次，再按间隔定时；interval=0 仅手动。
- 单对象在途限制：定时与手动共享，在途时跳过定时触发（不排队、不产生样本）；不补发停机期间探测。
- 20 对象 × 1 分钟间隔互不阻塞；每对象最多 1 在途请求。

### R4 统计与状态（§3.4、§7.2）
- 统计分母 = 已完成、非 cancelled 的定时探测；手动不进默认统计。
- 成功率+超时率+错误率=100%（互斥穷尽）；超时率区分两个子类；无样本时比例为 null、面板显示"暂无样本"。
- 时间窗 1h/24h/7d 按 started_at 筛选；默认仅当前 revision。
- 状态优先级：停用 > 仅手动 > 未知（当前版本无有效定时结果） > 结果过期（> 2×interval+timeout） > 可用（最新 ok，TTFT 超慢阈值附加提示） > 不可用（最新失败+原因）。
- 手动结果不刷新定时新鲜度；当前状态不由历史成功率推导。

### R5 存储（§5.3、§5.5、§9）
- 配置 JSON 文件（0600）；结果 JSONL 追加（每行含 §9.2 全字段；定时探测不存 output_preview；ttft_ms 未知为 null）。
- 每对象默认保留 20,000 条，可配，超限裁剪最旧；追加不重写全文件，裁剪与追加协调不丢新记录。
- 查询基于内存索引，不在每次刷新时全量扫描。
- 损坏行跳过并提示，不清空重建；存储失败显式提示。

### R6 Web 面板与 API（§4.3-4.6、§10、§11）
- 概览：名称/模型、监测状态（文字+色）、最近定时结果与时间、成功数/总数、三率。
- 详情：TTFT/总耗时趋势（内嵌 SVG，无第三方库/CDN）、可筛选失败明细、revision 与时间窗切换。
- 手动测试：立即测试按钮 → 最终结果+TTFT+总耗时+输出片段；在途时提示"正在探测"；停用仍可手动。
- 周期刷新只读不触发探测；状态与历史比例分开展示。
- API 按需求 §10 表实现；明细分页倒序，游标区分同时间戳，limit 有服务端上限。

### R7 非功能（§5）
- 单一可执行文件，无运行时依赖；前端内嵌；Windows/Linux/macOS 构建。
- 双击/一条命令启动，监听后自动开浏览器；默认 10110，占用 +1，--port 覆盖；仅监听 127.0.0.1。
- 数据存用户配置目录；离线可看配置与历史。
- Key 明文存储但不经 API 返回（仅 set 标志+掩码；PUT 未提供=保留，空串=清除）；Key 不入日志/错误/输出片段。
- 写操作校验 Origin（带且非本机 → 403；无 Origin 放行）；不开放 CORS。
- 平滑退出：停调度、取消在途、保存已完成结果。
- 单对象失败不影响进程与其他对象。

## Acceptance Criteria（对应 §12，全部可测）

### A1 探测判定与统计（可控模拟接口）
- [ ] A1.1 正常文本流按 [DONE] 结束记 ok；finish_reason=length 正常结束亦成功；缺 usage 不影响。
- [ ] A1.2 先角色/空事件后延迟文本：空事件不计 TTFT、不绕过首内容超时；TTFT 与文本到达时间一致（容忍计时误差）。
- [ ] A1.3 首内容超时与中途总超时分别归类 timeout_ttft/timeout_total；同一请求只计一次失败；同时到期无内容归 timeout_ttft。
- [ ] A1.4 HTTP 401/429/500 → http_error；200 中错误事件 → stream_error；无效协议数据 → protocol_error。
- [ ] A1.5 部分文本后异常断开或无结束证据 EOF → aborted；有 finish_reason 正常 EOF → 成功；正常结束无文本 → empty。
- [ ] A1.6 样本 3 定时成功 + 2 定时超时 + 1 定时错误 + 1 取消 + 1 手动成功：默认统计分母 6、成功率 50%、超时率 33.33%、错误率 16.67%；取消与手动不影响。
- [ ] A1.7 无样本时比例为 null/面板"暂无样本"；未知、停用、仅手动、过期状态不显示正常，不追加虚构失败。
- [ ] A1.8 换地址或模型后新版本从无样本开始；旧版本历史可查且不混入新版本统计。

### A2 调度、持久化与部署
- [ ] A2.1 定时/手动冲突时单对象最多 1 在途；不积压、不重试、不补发。
- [ ] A2.2 ≥20 对象 × 1 分钟调度互不阻塞；关浏览器后台继续。
- [ ] A2.3 重启后配置、历史、统计一致；损坏行不丢全部历史；裁剪后新记录保留且不超上限。
- [ ] A2.4 Windows 构建产出 .exe，无 Go/Python/Docker 环境双击可启动并打开面板；端口占用回退并展示实际 URL。
- [ ] A2.5 无外网环境面板可用；远端探测网络失败按规则记录。
- [ ] A2.6 默认仅监听 127.0.0.1；读取接口/日志/错误/输出片段不泄露完整 Key；跨域页面不能改配置或触发探测。
- [ ] A2.7 平滑退出保存已完成结果；存储异常明确提示，不归因被测接口。

## Out of Scope（§1.4、决策 #10）

- 语义质量评估、并发压测/RPS/TPS/token/分位数/排名、工具调用与多模态探测、自托管推理、多协议（非 OpenAI 兼容）、推送告警、CSV/JSON 导出、局域网开放、权限系统、多租户、多机采集、SQLite（MVP 后评估）。

## Technical Notes（约束性选型，详载 design.md）

- Go 单模块：cmd/llm-monitor + internal/{engine,store,server,probe}；go:embed 内嵌 web/。
- 存储目录 os.UserConfigDir()/llm-monitor；配置 0600。
- 前端单页原生 HTML/JS/CSS + 手写 SVG 趋势图，无第三方库。

## Open Questions

无——需求文档 v0.2 已定稿全部产品决策；技术实现决策见 design.md。
