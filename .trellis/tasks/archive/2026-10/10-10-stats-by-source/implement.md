# 执行计划:统计样本跟随来源筛选

- [x] 1 store:四函数加 source 参数;inWindow 三参 → A1/A2 fixture 测试通过
- [x] 2 server:querySource 助手(缺省 all/400);stats/series 接参 → A3 测试(过滤/缺省/400)
- [x] 3 前端透传:web refreshDetail 带 curSource;TUI detail 带 filters.source;报告统计跟随报告 source;ViewOf 概览卡显式 scheduled → TUI manual/all 渲染测试通过
- [x] 4 文档:REQUIREMENTS §3.4/§10 两行/决策 #14(新增,未改 #5 原文——#5 讲默认口径仍然成立);README 统计口径段同步
- [x] 5 全量门:gofmt/vet/test -race 全绿;单提交 c9872e0;二进制已重编译
- [ ] 6 手动冒烟(A4/A5,用户执行):web 切来源统计变化;TUI `S` 切来源统计变化;徽章不变
