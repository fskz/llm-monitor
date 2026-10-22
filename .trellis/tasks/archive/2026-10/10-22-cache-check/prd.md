# 前缀缓存检测(cache-check)

## Goal

新增手动触发的前缀/prompt 缓存检测:两次同 prompt 请求,以 usage.cached_tokens 为主证据、TTFT 差分为辅助证据,输出三态结论;TUI /cache 命令与 Web 详情页双端交付,结果独立持久化,不与现有探测共享 in-flight 槽位。

## 背景与问题

监控的模型接口(当前两个 provider 均经本地中转 `localhost:10100/v1`)是否在服务端启用了前缀/prompt 缓存,目前无从得知。缓存状态直接影响真实使用成本(缓存命中侧输入通常计费打折)与 TTFT 预期,被中转悄悄关闭缓存是常见的服务质量陷阱。需要一个可解释、可复现的检测手段。

术语约定:本任务 "prefill" 指用户口语中的"前缀缓存"能力;仓库内已有的 `prefill_tps` 指标(提示词侧吞吐,`internal/store/stats.go:70`)含义不同,检测功能命名一律避开 `prefill`,使用 `cache` / `cache-check` / 中文标签"前缀缓存"。

## Requirements

- **R1 三态判定**:检测结论只能是 已开启 / 未开启 / 无法判定 / 检测失败 四者之一。
  - 主证据:两次请求的 `usage.prompt_tokens_details.cached_tokens`(强制带 `stream_options.include_usage`,不受 provider 的 `include_usage` 配置影响)。
  - 字段存在且任一跳 >0 → 已开启;字段存在且两跳均为 0 → 未开启;字段缺失(被中转剥离)→ 无法判定,附 TTFT 差分作为辅助证据文本(仅供参考,不参与结论)。
  - 检测请求自身失败(HTTP 错误/连接错误/流错误/超时)→ 检测失败,注明第几跳与原因;与"无法判定"严格区分。
- **R2 检测执行**:固定两次相同 prompt 的顺序请求(冷 + 热),不做提前短路;使用内置确定性长 prompt(≥2048 token 量级,覆盖常见缓存激活门槛);`max_tokens` 压到个位数(≤8)控制成本。
- **R3 入口(双端)**:
  - TUI:`/cache` 命令,作用于当前选中 provider(`needSel` 语义)。
  - Web:详情页手动探测框旁新增"前缀缓存检测"按钮 + 结果卡片。
- **R4 独立持久化**:每个 provider 仅保留最近一次判定(时间戳、结论、证据摘要),独立存储文件;不写入 `results/<id>.jsonl`、不进入任何统计/健康状态/overview;应用重启后仍在,TUI detail 与 Web 详情页常驻展示最近判定。
- **R5 独立模块约束**(对应用户 Q10/Q11 决策):
  - 不与定时探测/手动探测共享 in-flight 槽位;检测可与其并发运行。
  - 检测自身需有独立互斥:同一 provider 检测进行中重复触发 → 明确冲突反馈(409/提示),不同 provider 互不影响。
  - 对 disabled / manual-only provider 同样可手动执行(与 `/probe` 行为对齐)。
  - 检测使用自身固定总超时预算(不走探测的 TTFT 超时中断;TTFT 只是证据,不是中止条件)。
  - 已知取舍:与定时探测并发时 TTFT 差分辅助证据可能被污染,接受(主证据 usage 不受影响),结果文案注明 TTFT 仅供参考。
- **R6 复用边界**:底层 HTTP 请求与 SSE 解析能力按库级复用(纯函数/解析器),但执行编排、状态、持久化均为新增,不修改现有 `probe.Do` 单发语义与 engine in-flight 结构。

## 非目标(本期不做)

- 周期自动巡检 / 批量全量检测(仅保留手动单 provider 触发;后续扩展)。
- 检测 prompt 长度可调 / 用户自定义检测 prompt。
- assistant prefill(助手消息预填充)能力检测。
- 缓存检测结果的历史曲线 / 多次留存。

## Acceptance Criteria

- [ ] A1 mock 端点返回 `cached_tokens > 0`:判定"已开启",证据含两跳的 cached_tokens 数值。
- [ ] A2 mock 端点字段存在且恒为 0:判定"未开启"。
- [ ] A3 mock 端点剥离 `prompt_tokens_details`:判定"无法判定",附两跳 TTFT 数值与差分提示。
- [ ] A4 mock 端点请求级失败(非 2xx / 连接拒绝 / 流中断 / 超时):判定"检测失败",注明跳数与原因。
- [ ] A5 TUI `/cache` 与 Web 按钮均可触发并展示结果;结果持久化,重启后 TUI detail / Web 详情页仍显示最近判定。
- [ ] A6 独立性回归:检测执行期间,同 provider 的定时探测与 `/probe` 正常运行不被阻塞;检测完成后现有统计、健康状态、overview 无任何变化;`results/<id>.jsonl` 无新增行。
- [ ] A7 同一 provider 检测进行中重复触发 → 冲突反馈;disabled provider 可正常执行检测。
- [ ] A8 命名检查:新增代码/命令/JSON 字段/文案中不出现与 `prefill_tps` 语义混淆的 `prefill` 命名。

## Notes

- Q11 用户答复"独立功能模块,不影响"的实现解读(待用户评审确认):① 检测自带固定总超时(设计拟 60s/跳),不沿用 provider 的 TTFT 超时中断;② disabled provider 可执行;③ 检测结果不回流任何现有健康/统计。
- 决策来源:grilling 会话 2026-10-22(Q1=B 前缀缓存;Q2 单 model;Q3 手动 MVP;Q5=C 混合三态;Q6=A 内置长 prompt;Q7=C 独立持久化;Q8=A /cache 命名;Q9=B 固定两次;Q10=B 独立模块;Q11 独立不影响;Q12=A 双端)。
