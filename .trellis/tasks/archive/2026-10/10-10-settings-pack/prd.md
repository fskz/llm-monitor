# 全局设置包:默认值预填 + 连败阈值提示 + 报告路径

> 来源:grill-me 拷问(2026-10-10)配置侧决议。三项已定:全局默认值(仅预填新建)、连续失败阈值本地高亮(不推送)、报告导出路径可配置;prompt 模板与多 endpoint 轮询明确不做。

## Goal

工具级设置进入 config.json(新增 `settings` 段),面板设置区 + TUI 设置模态双端可改;三项设置各自落地:新建表单预填默认值、连败 ≥N 本地视觉提示、报告导出目录可配置。

## Background / Confirmed Facts(代码取证)

- config.json 结构:`{version, next_id, max_results_per_provider, providers}`(store.go:105-110);新增 `settings` 段为向后兼容增量(缺省=内置默认),旧文件加载不受影响。
- 表单默认值现在硬编码于两处:web `openProviderDialog` 的 else 分支(app.js)与 TUI `openForm` 的初值(forms.go);两处需改为读设置。
- 连败计数 `ProviderView.StreakFail` 已在 10-10-metrics-pack 落地(概览角标 ≥2 固定);阈值提示=把固定 2 换成可配置 N,并加高亮样式。
- 报告路径:TUI `exportReport`(actions.go)硬编码 `<dir>/reports/`;web 端报告是浏览器下载不落盘,路径设置仅影响 TUI 导出。
- 设置校验可复用 view.ProviderForm 的规则子集(interval/timeout/max_tokens/慢阈值)。

## Requirements

### R1 设置模型与存储

- config.json 新增 `settings` 段,字段:`defaults`(ProviderForm 的数值/布尔子集:name/base_url/model/prompt 不预填——留空让用户填)、`streak_alert`(int,默认 2,≥2 有效)、`report_dir`(string,空=默认 `<data>/reports/`)。
- 缺省行为:段缺失/字段缺失 → 内置默认;坏值(如 streak_alert=0)→ 加载时回退默认并日志提示,不阻塞启动。
- store 提供 `GetSettings()/SaveSettings()`(原子写,与 config.json 同锁同文件)。

### R2 API 与双端入口

- `GET /api/settings` 返回当前设置(不含任何敏感值——本就无);`PUT /api/settings` 全量更新,校验失败 400 中文消息;写端点 sameOrigin 保护。
- web:顶部「设置」按钮 → 设置对话框(三个分区:默认值表单/阈值/路径)。
- TUI:全局键(建议 `,` 或 `o`,避开现有键位)→ 设置模态表单;保存后立即生效。

### R3 默认值预填(仅新建)

- web `openProviderDialog`(无 id)与 TUI `openForm`(editID=0)的初值改读 settings.defaults。
- **不提供批量应用到已有对象**(拷问决议,误操作风险);编辑已有对象时预填其自身值,行为不变。

### R4 连败阈值提示

- 概览角标阈值从固定 2 改为 settings.streak_alert;达标时角标 + 卡片边框高亮(web `.card.alerting` 红边;TUI 列表项红色)。
- 仅本地视觉,**不做推送/声音/系统通知**(维持决策 #3)。
- 阈值下限 2(1 会与单次失败无法区分,失去"真挂了"语义)。

### R5 报告路径

- settings.report_dir 非空 → TUI 导出到该目录(自动创建);空 → 现状 `<data>/reports/`。
- 路径校验:保存时尝试 mkdir,失败 400;导出时再兜底。

## Acceptance Criteria

- [ ] A1 旧 config.json(无 settings 段)加载 → 全部默认值,无错误(store 单测)。
- [ ] A2 SaveSettings 原子落盘,重启后保留;坏值回退默认+日志(store 单测)。
- [ ] A3 GET/PUT /api/settings 往返一致;无效值(阈值 1、路径不可写)400 中文消息;跨域写 403(server 单测)。
- [ ] A4 设置 defaults 后新建表单(web+TUI)预填该值;编辑已有对象不受影响(冒烟)。
- [ ] A5 阈值改 5 → 连败 3 不显角标、连败 5 显角标+高亮(渲染测试或冒烟)。
- [ ] A6 report_dir 设置后 TUI `r` 导出到该目录;清空回到默认(测试)。
- [ ] A7 回归:gofmt/vet/`go test ./... -race` 全绿;README 设置节 + REQUIREMENTS §5.2/决策 #15 补充。

## Out of Scope

- 批量应用默认值到已有对象;推送/声音告警;prompt 模板;多 endpoint;web 端报告落盘路径(浏览器下载语义不变)。

## Technical Notes

- 建议新文件:store/settings.go(模型+读写)、server/settings_api.go、tui/settings.go(模态)、web 设置对话框。
- web 设置对话框复用 provider 对话框样式;TUI 复用 formDialog 模式。
- 阈值高亮与 metrics-pack 的 streak_fail 数据衔接,无新计算。

## Open Questions

- 无(三项拷问已决,入口/默认值/阈值边界如上锁定)。
