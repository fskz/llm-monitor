# 技术设计 — HTML 监控报告导出

> 需求：本任务 prd.md。数据源全部复用现有 store/server 查询。

## 1. 架构与边界

```
web/report.tmpl          内嵌报告模板（go:embed 到 internal/server）
internal/server/report.go   新文件：handleReport + 模板 funcs（SVG 折线、时间格式化、slug）
web/app.js               详情页"导出报告"按钮（window.open 组 URL）
```

- 不新增 store 方法；报告数据 = viewOf(p) + Stats + Series + QueryResults 的组合。
- 模板经 html/template.Execute 渲染，`Funcs` 注入 svgPolyline/fmtTime/fmtMs/fmtTPS/fmtPct。
- 零新依赖（标准库 text/template 转 html/template、embed 已在用）。

## 2. 路由与响应

```
GET /api/report?provider=&revision=&window=&source=
```

- queryProvider 复用（provider/revision/window 校验与 400/404 语义一致）。
- source：缺省 "all"；非法值 400。
- 响应头：`Content-Type: text/html; charset=utf-8`；`Content-Disposition: attachment; filename="<slug>-<window>-<yyyymmdd-hhmm>.html"`。
- slug 规则：小写化 → 非 [a-z0-9]+ 折叠为 `-` → trim `-`；空串 → `provider-<id>`。

## 3. 报告数据组装（handleReport 内）

```go
type reportData struct {
    GeneratedAt string            // 本地时区 yyyy-MM-dd HH:mm:ss
    View        providerView      // 复用概览视图（含状态/掩码/last_probe）
    Filters     string            // "时间窗 24h · 目标版本 v2 · 来源 全部"
    Stats       statsView         // 复用 /api/stats 映射（含吞吐）
    OKChart     svgChart          // 成功率
    LatencyChart svgChart         // TTFT+总耗时双线
    Rows        []reportRow       // ≤200
    Truncated   bool              // 实际行数 == 请求上限且仍有更多
}
type reportRow struct {  // store.Result 的展示投影
    Time, Source, ResultIcon, StatusText, TTFT, Total, HTTP, Tokens, Error string
}
```

- 明细：`QueryResults(p.ID, rev, source, window, 201, nil, nil)`；len>200 → Truncated=true，截 200。
- 状态中文名/图标：Go 侧新 statusText() 映射（与 web/app.js STATUS_TEXT 同表——**两处同步**，写入规范）。

## 4. SVG 生成（模板 funcs）

- `svgPolyline(buckets, key, w, h, ...) string`：移植 web/app.js bucketPath 数学（等分 x、按 max 值归一 y、null 断开为多段 polyline）；返回 template.HTML（几何数字自算无注入面，坐标值经 strconv 格式化）。
- 两图尺寸 640×160；成功率固定 0-100%；耗时图 max 取桶内最大 avg_total（下限 1000ms 同前端）。
- 空数据：模板层判断渲染"暂无样本"占位，不调 polyline。

## 5. 模板结构（report.tmpl）

```
<!doctype html><html lang=zh-CN><meta charset=utf-8>
<title>监控报告 · {对象名} · {窗口}</title>
<style> ~80 行内联（打印友好：@media print 隐藏按钮提示、表格斑马纹）</style>
<header> 工具名 / 对象 / 模型 / 版本 / 生成时间 / 过滤回显 / 目标快照 </header>
<section 状态> 徽章（六态色 class 同面板命名）+ 最近定时探测 </section>
<section 统计> 卡片行：三率/样本/均值/吞吐 </section>
<section 趋势> 两个 <svg>（funcs 注入 polyline）</section>
<section 明细> 表格 ≤200 行 + 截断说明 </section>
<footer> 快照说明 </footer>
```

- 全部动态值走 {{ }} 默认转义；svgPolyline 返回值是唯一 template.HTML（纯数字/字母几何串）。

## 6. 前端按钮

```js
$("btn-export").addEventListener("click", () => {
  const qs = `provider=${currentId}&revision=${encodeURIComponent(revisionParam())}` +
             `&window=${curWindow}&source=${curSource}`;
  const a = document.createElement("a");
  a.href = `/api/report?${qs}`;   // Content-Disposition 触发下载，无需 download 属性
  document.body.appendChild(a); a.click(); a.remove();
});
```

- index.html toolbar 加 `<button id="btn-export" class="ghost">导出报告</button>`。

## 7. REQUIREMENTS.md 修订

| 位置 | 修订 |
|------|------|
| §4.5 | "CSV / JSON 导出列为后续增强" → "支持导出单对象 HTML 监控报告（自包含、可打印）；CSV / JSON 为后续增强" |
| §10 | 表尾补 `GET /api/report` 行（参数同 stats+source，attachment HTML） |
| §13 | M3 内容中标注 HTML 报告已交付 |
| §14 | 决策 #12：单对象 HTML 报告、attachment 下载、明细 ≤200、source 默认 all、不含输出片段与 Key |

## 8. 权衡

| 决策 | 备选 | 取舍 |
|------|------|------|
| 服务端 Go 模板渲染 | 前端 JS 拼 HTML 下载 blob | 单一可信转义点（html/template）；URL 可直接分享；前端仅一个链接 |
| 不展示 output_preview | 展示 | 报告会被分享/存档，输出片段是接口生成内容，非可用性必需 |
| source 默认 all | scheduled | 报告是事后存档，手动/取消记录有分析价值；行内来源列可筛看 |
| ≤200 行硬上限 | 全量 | 20k 行单文件几 MB 且打印不可用；截断注明 |
| 复用 providerView | 新建 reportView | 字段全够（掩码/状态/last_probe），避免两份漂移 |

## 9. 测试（映射 A1-A8）

- server 单测：A1（头/文件名/400/404）、A3（300 条→200+Truncated）、A4（`<script>` 转义、Key 不出现）、A5（无外链资源引用）、A6（与 /api/stats 同 fixture 样本数一致）。
- 模板渲染冒烟：A2 断言关键 section 存在（徽章/统计卡/svg/表格/失败原因列）。
- A7 手动冒烟（make build + 详情页按钮）。
- 回归 A8 全量 -race。

## 10. 回滚

单 commit 功能（report.go + report.tmpl + 按钮两行 + 文档）；revert 即回滚，无数据迁移。
