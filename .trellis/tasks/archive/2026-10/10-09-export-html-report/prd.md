# 导出 HTML 监控报告

> 来源：用户请求"增加导出监控报告功能"（2026-10-09），形态已确认：单文件自包含 HTML。基线修订：REQUIREMENTS.md §4.5 将导出列为后续增强——本任务把 **HTML 报告**提前启用（CSV/JSON 仍后置）。

## Goal

在详情页一键导出**单个监测对象**的自包含 HTML 报告：概览摘要、状态、三率、吞吐、趋势图、探测明细与失败原因，内嵌样式与 SVG（零外部资源、离线可开、可打印/另存 PDF），便于汇报与分享。

## Background / Confirmed Facts（代码取证）

- 路由表 internal/server/server.go:65-73；查询助手 queryProvider（provider/revision/window 解析）在 stats_api.go:102-126。
- 服务端已有数据源：viewOf（配置摘要+状态+last_probe+24h stats）、Stats、Series（12/24/28 桶）、QueryResults（倒序+游标，limit 上限 500）。
- 前端图表为手写 SVG（web/app.js:221-286 bucketPath/xLabels），数学简单可移植到 Go 模板函数。
- 仓库无 html/template 使用先例，但 Go 标准库自带（零依赖约束不破）。
- 错误详情/输出预览是不可信服务端数据，报告必须转义（html/template 自动转义满足；规范 go-conventions 要求 esc 纪律）。
- API Key 绝不入报告（viewOf 已只输出 set+掩码）。

## Requirements

### R1 API
- `GET /api/report?provider=&revision=&window=`：参数与 /api/stats 完全一致（queryProvider 复用）；响应 `Content-Type: text/html; charset=utf-8` + `Content-Disposition: attachment; filename="llm-monitor-<对象名slug>-<window>-<yyyymmdd-hhmmss>.html"`。
- filename 对象名做 slug 化（非 [a-z0-9-] 折叠），空则用 provider-<id>；中文对象名常见，slug 后可能为空 → fallback。
- 明细行数上限 200（服务端硬上限，超出在报告中注明"仅展示最近 200 条"）；source 固定 `all`（报告是快照存档，手动/取消记录对事后分析有价值，且行内有来源列可区分）。→ 修订：source 参数接受 `scheduled|manual|all`，缺省 `all`（报告存档语义），与 /api/results 一致。
- 同源要求：报告是 GET 且只读，不加 Origin 限制（可直接浏览器打开/分享链接）。

### R2 报告内容（自上而下）
1. 头部：工具名、对象名/模型/当前版本、报告生成时间（本地时区）、过滤条件回显（时间窗/目标版本/来源）、目标快照（探测时 base_url/model，取明细最新一条）。
2. 监测状态徽章（六态文字+色，规则同面板）+ 最近一次有效定时探测（状态/时间/TTFT/总耗时）。
3. 统计卡：样本数、成功/超时/错误计数与三率（无样本显示"暂无样本"，比例空白——沿用 null 口径）、平均 TTFT/总耗时、decode/prefill 吞吐（prefill 标"近似"）。
4. 两张 SVG 趋势图（成功率、成功耗时 TTFT+总耗时），复用 Series 桶与前端同款坐标数学；空桶断开、无样本显示占位文案。
5. 探测明细表（倒序 ≤200 行）：开始时间、来源、结果图标、状态中文名、TTFT、总耗时、HTTP、token 数（有 usage 时）、错误说明。行内不展示 output_preview（报告可能被分享，输出片段含被测接口内容，仅保留错误说明；→ 修订：不展示 preview，报告中亦不包含 Key——已由 viewOf 保证）。
6. 页脚：生成工具版本标识、数据窗口说明、"报告为时点快照，统计口径见工具面板"。

### R3 前端
- 详情页 toolbar 增加"导出报告"按钮：以当前 时间窗/版本/来源 组装 URL，`window.open`（GET attachment 触发下载；浏览器拦截弹窗时 fallback `<a download>` 点击）。
- 概览卡片不加导出（范围控制；单对象语义与详情一致）。

### R4 安全与兼容
- html/template 自动转义全部动态值；CSS/SVG 内联；无外链、无 JS 依赖（纯静态可打印）。
- API Key 不出现在报告任何位置。
- 中文对象名/错误信息 UTF-8 正常渲染（charset 声明）。

### R5 需求文档同步
- §4.5：导出条款修订（HTML 报告纳入；CSV/JSON 仍后置）。
- §10 API 表补 `GET /api/report` 行。
- §14 决策 #12（HTML 单对象报告、attachment 下载、明细 ≤200、source 默认 all）。
- §13 M3 行提及 HTML 报告已提前交付。

## Acceptance Criteria

- [ ] A1 `GET /api/report?provider=&window=24h` 返回 200、text/html、attachment 头、文件名含对象 slug 与时间戳；无 provider/坏参数 → 400/404 与 /api/stats 一致。
- [ ] A2 报告含：状态徽章、三率（无样本时"暂无样本"）、均值、吞吐（prefill 标近似）、两张 SVG、明细表 ≤200 行带失败原因列。
- [ ] A3 明细 300 条时报告只含最新 200 条且有截断说明。
- [ ] A4 注入安全：对象名/错误串含 `<script>` 时报告源码中为转义文本（html/template）；API Key 不出现在报告中（构造带 Key 对象+错误含 Key 的场景 grep 断言）。
- [ ] A5 报告无任何外部 URL（grep http://|https:// 仅允许出现在错误详情文本中，不作为资源引用）；`<style>` 与 `<svg>` 内联。
- [ ] A6 时间窗/revision 过滤与 /api/stats 同参数结果一致（同一 fixture 两接口对比样本数）。
- [ ] A7 详情页"导出报告"按钮用当前过滤条件触发下载（手动冒烟）。
- [ ] A8 全量回归：gofmt/vet/`go test ./... -race` 全绿；REQUIREMENTS.md 修订完成。

## Out of Scope

- CSV / JSON 导出（仍按基线后置）；多对象汇总报告；定时自动生成/邮件发送；报告主题切换；导出配置记忆。

## Technical Notes

- 实现细节（模板结构、SVG 生成、filename slug 规则）见 design.md。
- 复用面大：queryProvider/viewOf/Stats/Series/QueryResults 全部现成，新增主要是模板与路由。

## Open Questions

无（单对象范围、source=all 默认、≤200 明细均为已定决策，见决策 #12 草案）。
