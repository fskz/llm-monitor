# 统计样本与指标跟随来源筛选

> 来源:用户反馈(2026-10-10)——5 条明细(4 手动 + 1 定时)时统计卡只有 1 条样本的指标,被判定为困惑点。用户决策:**统计样本应根据所选来源确定**(筛什么统计什么)。

## Goal

详情页的统计卡与趋势图跟随「来源」筛选器:选定时 → 仅定时样本;选手动 → 仅手动样本;选全部 → 两者合并。监测状态徽章不受影响(仍由最后一次有效定时探测决定,§7.2)。

## Background / Confirmed Facts(代码取证)

- 分母口径硬编码于 `internal/store/stats.go` 的 `inWindow`(L195-199):`r.Source == SourceScheduled && r.Status != statusCancelled`,被 `Stats`/`AvgOnOK`/`AvgThroughput`/`Series` 四个函数共用。
- server 端 `/api/stats`、`/api/series`(stats_api.go:80-100)不接收 source 参数;`/api/results` 已接收(source 缺省 scheduled)。
- web 端 `refreshDetail`(app.js:165-174)拉 stats/series 时不带 source,拉 results 时带 `curSource`。
- TUI 端 detail.go:140-164 同样调 Stats/AvgOnOK/AvgThroughput/Series 无 source。
- 需求 §3.4 首条写死"默认统计仅包含…定时探测";决策 #5 同口径;§10 表 `/api/stats` 无 source 参数。

## Requirements

### R1 口径变更(核心)

- `Stats`/`AvgOnOK`/`AvgThroughput`/`Series` 增加来源过滤参数:scheduled / manual / all。
- **监测状态与 last_probe 不变**:§7.2 状态阶梯仍基于定时探测(状态是"调度健康",与用户筛看哪个来源正交)。Overview 卡片(24h/当前版本/定时)同样不动——只有详情页统计跟随筛选。
- 三率互斥穷尽关系在任一来源子集内保持;cancelled 仍从分母剔除(取消不是结果);无样本仍显示"暂无样本"。

### R2 API

- `/api/stats`、`/api/series` 接受 `source` 参数,取值 scheduled|manual|all;**缺省 all**(与 `/api/results` 缺省 scheduled 不同——见 Open Questions 已决)。
- 无效 source → 400(与 /api/results 的校验消息一致)。

### R3 前端

- web:`refreshDetail` 的 stats/series 请求带上 `curSource`;筛选器切换时统计随之刷新(现有 change 监听已统一走 refreshDetail)。
- TUI:detail.go 的统计/sparkline 查询带上 `d.filters.source`(筛选状态已存在,透传即可)。

### R4 需求文档

- §3.4 首条改为:默认(无筛选)视图仅定时;详情页统计随来源筛选切换,手动/全部时手动样本计入所选视图的分母与均值;监测状态仍仅由定时探测决定。
- §10 表两行补 source 参数。
- 决策 #5 补充修订说明(2026-10-10:来源筛选作用于统计视图;默认口径不变)。

## Acceptance Criteria

- [ ] A1 同一 fixture(3 定时成功 + 2 手动成功 + 1 定时超时):source=scheduled → 样本 4、成功 3;source=manual → 样本 2、成功 2;source=all → 样本 6、成功 5(store 层单测)。
- [ ] A2 取消样本在任何 source 下都不进分母。
- [ ] A3 `/api/stats?source=manual` 与 `/api/series?source=all` 各自生效;缺省 all;无效值 400(server 层测试)。
- [ ] A4 web 详情切换来源,统计卡数字随之变化(手动验证);导出报告仍按报告自身 source 参数(缺省 all,不变)。
- [ ] A5 TUI 详情 `S` 键切换来源,统计与 sparkline 随之变化。
- [ ] A6 监测状态徽章在任何筛选下不变(仍定时口径)。
- [ ] A7 回归:gofmt/vet/`go test ./... -race` 全绿;REQUIREMENTS §3.4/§10/决策#5 修订完成。

## Out of Scope

- 监测状态/Overview 卡口径变更;手动样本权重;报告导出口径;吞吐公式调整。

## Technical Notes

- 实现顺序与文件清单见 design(本任务较直接,PRD 之外仅补一份精简 design 说明参数流即可)。

## Open Questions

- `/api/stats` 缺省值:已决 all(与 /api/results 的 scheduled 不同是有意的——REST 面向"查询该来源的统计",而 results 缺省是历史遗留面板默认;文档中注明差异)。
