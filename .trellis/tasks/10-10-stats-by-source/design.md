# 技术设计:统计样本跟随来源筛选

> 任务直接(单参数贯穿四层),无独立 design 必要,此文件记录参数流与关键取舍。

## 参数流

```
web (curSource) ──┐                        TUI (d.filters.source) ──┐
                  ▼                                                   ▼
GET /api/stats?…&source=   │  GET /api/series?…&source=              d.st.Stats/Series/…(source)
                  ▼                                                   │
server stats_api.go: parse source (缺省 all, 无效 400)                │
                  ▼                                                   ▼
store.Stats / AvgOnOK / AvgThroughput / Series (…, source string)
                  ▼
inWindow(r, minStartedAt, source): r.Source 匹配 && !cancelled && 窗口内
```

## 关键点

1. **store 签名**:四个函数统一加 `source string` 尾参;`SourceAll` 复用现有常量。`inWindow` 改三参(内部函数,改动封闭)。AvgOnOK/AvgThroughput 的 `statusOK` 分支同样受 source 约束(成功均值也按来源)。
2. **报告导出不受影响**:`view.RenderReport` 已接收 source(缺省 all 语义),内部调 Stats 系列时透传该 source——报告本来就该按报告自身参数统计。**这是行为变更点**:此前报告 stats 恒为定时口径,现在跟随报告 source 参数(缺省 all 时手动样本将计入报告统计)。§3.4 修订与 A4 验收覆盖此点。
3. **server**:queryProvider 旁新增 sourceOf(w, r) 小助手(缺省 all,校验三值),stats/series 两个 handler 使用。
4. **web**:refreshDetail 的 qs 拼上 `&source=${curSource}`(stats/series 两个请求)。
5. **TUI**:detail.go 三处调用 + Series 加 `d.filters.source`。
6. **监测状态**:view.ViewOf 的 monitorStatus 走 LatestValidScheduled,不经 inWindow,天然不受影响——不改。

## 测试

- store:fixture 驱动 A1/A2(三 source × 取消样本)。
- server:httptest 断言 source 过滤生效 + 400 分支。
- view:report 渲染在 source=manual fixture 下统计含手动样本(A4 的自动部分)。
- TUI:detail_test 补一例 manual 筛选下样本数断言。
