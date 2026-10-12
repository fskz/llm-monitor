# 执行计划:全局设置包

- [ ] 1 store/settings.go:Settings 模型(defaults/streak_alert/report_dir)+ GetSettings/SaveSettings(与 config.json 同锁原子写)+ 坏值回退 → 单测 A1/A2
- [ ] 2 server:GET/PUT /api/settings(sameOrigin,校验 400)→ 单测 A3
- [ ] 3 web:设置对话框(三分区)+ 顶栏入口;openProviderDialog 新建分支改读 settings.defaults;角标阈值/高亮读 settings → A4/A5 web 半
- [ ] 4 TUI:settings.go 设置模态(新全局键)+ openForm 新建初值改读设置 + 列表项连败高亮 → A4/A5 TUI 半
- [ ] 5 报告路径:exportReport 读 settings.report_dir(空=默认,自动 mkdir)→ A6
- [ ] 6 文档:README 设置节、REQUIREMENTS §5.2/§10/决策 #15
- [ ] 7 全量门:gofmt/vet/`go test ./... -race`;单提交;重编译
- [ ] 8 冒烟(用户):双端改设置即生效;预填/阈值/路径三链路
