# 执行计划:指标增强包

- [ ] 1 store 聚合函数:`Percentiles`(TTFT/Total 各 P50/P95,最近邻秩+偶数取下中位)、`Streaks`(连败/连成,cancelled 跳过)、`ErrorBreakdown`(10 类计数)、`FlipRate`(翻转/对数);统一 source 口径 → 单测 A1-A4
- [ ] 2 view/API:StatsView 扩展字段(snake_case,nullable);/api/stats 响应带上;overview 卡连败角标数据(ProviderView 加 streak 字段)→ server 测试
- [ ] 3 web:统计卡加 P50/P95/抖动行、错误分布摘要行、概览卡连败角标(≥2 显示)
- [ ] 4 TUI:detail 统计块扩展(分位数/分布/抖动/连败)
- [ ] 5 报告:report.tmpl 统计卡区加卡(分位数、抖动)+ 状态旁连败 + 分布行;模板函数
- [ ] 6 文档:README 指标清单、REQUIREMENTS §3.1 表补行
- [ ] 7 全量门:gofmt/vet/`go test ./... -race`;单提交;二进制重编译
- [ ] 8 手动冒烟 A6(用户):切筛选随刷;三端可见
