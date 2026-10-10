# 执行计划:统计样本跟随来源筛选

- [ ] 1 store:Stats/AvgOnOK/AvgThroughput/Series 加 source 参数;inWindow 三参;现有调用点同步(编译器驱动);store 测试补 A1/A2 fixture → 验证:`go test ./internal/store -race`
- [ ] 2 server:sourceOf 助手(缺省 all/400);/api/stats、/api/series 接参透传 → 验证:server 测试 A3
- [ ] 3 前端透传:web refreshDetail qs 带 curSource;TUI detail.go 带filters.source → 验证:TUI 测试补 manual 断言;view 报告测试覆盖 source 行为(设计要点 2)
- [ ] 4 文档:REQUIREMENTS §3.4/§10/决策#5;README 统计口径段落同步 → 验证:通读一致
- [ ] 5 全量门:gofmt/vet/`go test ./... -race`;提交(单提交,改动内聚)
- [ ] 6 手动冒烟(A4/A5,用户执行):web 切来源统计变化;TUI `S` 切来源统计变化;徽章不变
