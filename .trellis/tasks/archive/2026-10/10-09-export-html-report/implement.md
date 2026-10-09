# 实施计划 — HTML 监控报告导出

> 改动集中（1 个新 handler 文件 + 1 模板 + 前端按钮 + 文档），主会话单线实现。

## 步骤

### S1 报告端点
- [ ] internal/server/report.go：handleReport（queryProvider 复用、source 缺省 all、QueryResults 201 截 200、组装 reportData）
- [ ] embed report.tmpl（//go:embed 放 internal/server，模板文件随之）；路由注册 GET /api/report（只读不加 Origin）
- [ ] 模板 funcs：svgPolyline（移植 bucketPath 数学）、fmtTime/fmtMs/fmtTPS/fmtPct/statusText；slug 化 filename
- 验证：`go test ./internal/server/ -race -count=1`（A1/A3/A4/A5/A6 新用例）

### S2 模板与样式
- [ ] report.tmpl 全结构（header/状态/统计/趋势/明细/页脚）+ 内联打印友好 CSS
- [ ] 空数据占位（"暂无样本"）；六态徽章配色沿用面板命名
- 验证：渲染冒烟断言关键节点（A2）

### S3 前端按钮
- [ ] index.html toolbar 按钮；app.js 组 URL 触发下载
- 验证：`node --check` + 手动冒烟（A7）

### S4 文档
- [ ] REQUIREMENTS.md §4.5/§10/§13/§14-#12

### S5 质量检查（trellis-check）
- [ ] A1-A8 逐条；全量 -race + gofmt/vet；转义与无外链复核

## 风险 / 回滚

| 风险 | 缓解 |
|------|------|
| svgPolyline 注入面 | 坐标仅 strconv 数字；唯一 template.HTML 点位注释说明 |
| 状态中文表两处漂移（Go/js） | 规范增补同步条目；check 复核 |

回滚：S1-S4 单 commit revert。

## 完成定义

A1-A8 全过；报告离线打开可打印、无外链、无 Key。
