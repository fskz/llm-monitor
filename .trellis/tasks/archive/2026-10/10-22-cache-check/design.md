# 技术设计:前缀缓存检测(cache-check)

对应 `prd.md`。以下所有现有代码引用来自 2026-10-22 探索报告,实现前以现场代码为准。

## 1. 模块边界与数据流

```
TUI /cache ─────────────┐
Web POST /api/providers/{id}/cache-check ─┤
                                        ▼
                        view.EngineAPI.CacheCheck(id)      (新增接口方法,view.go:49-56 追加)
                                        ▼
                        engine.Engine.CacheCheck(id)       (新方法,独立于 ProbeNow/in-flight)
                                        ▼
                        internal/cachecheck.Run(...)       (新包:编排两次请求 + 判定)
                          ├─ 复用 internal/probe 的 SSE 解析与请求构造(库级)
                          └─ 自带 prompt 常量 / 超时 / include_usage=true
                                        ▼
                        store.SaveCacheCheck(id, record)   (新文件,覆盖式最近一次判定)
                                        ▼
                        view.CacheCheckView ── TUI detail 区块 / Web 详情卡片
```

依赖方向遵循现有规则:`tui → view/store`(不 import engine/server,run.go:1-7);`server → view`;`engine → probe,store,cachecheck`。

## 2. internal/cachecheck(新包,判定核心)

- **纯函数风格,与 probe.Do 同纪律**:无全局状态、不重试、除编排所需的两次请求外不发额外请求。
- `Target`:`BaseURL/APIKey/Model/Timeout`(从 `store.Provider` 快照构造,仿 engine.runProbe engine.go:326-337;`Timeout` 用检测自身默认,见 §5)。
- **检测 prompt**:包内常量,确定性填充文本(固定段落重复拼接),长度 ≥2048 token 量级(覆盖 OpenAI 1024 档与常见更高门槛);`max_tokens = 8`。请求体沿用 OpenAI 兼容 `chat/completions` + `stream:true` + `stream_options.include_usage=true`(强制开启,不受 provider 配置影响)。
- `Run(ctx, client, Target) Result`:顺序发两次相同请求;每跳记录 `Status/HTTPStatus/TTFTMs/TotalMs/PromptTokens/CompletionTokens/CachedTokens(*int64,字段缺失为 nil)/ErrDetail`。
- **判定(verdict)**,纯函数、表驱动可测(2026-10-22 修订:与 prd R1 对齐——单跳确凿证据即足以判定):
  1. 任一跳 `Status != ok` → `failed`(注明跳数与 ErrDetail);
  2. 任一跳 `CachedTokens` 非 nil 且 >0 → `supported`(另一跳字段被剥不影响结论,证据文本标注 absent);
  3. 两跳字段均存在且均为 0 → `unsupported`;
  4. 其余(至少一跳字段缺失且无任何 >0)→ `unknown`,生成辅助证据文本:两跳 TTFT 数值 + 比值,措辞"仅供参考"。
- 中文标签归属 `internal/view`(状态标签表模式,status.go:7-31),cachecheck 包只出机器值。

## 3. store 持久化(新文件 internal/store/cachecheck.go)

- 每 provider 一个 JSON 文件 `<dir>/cachecheck/<id>.json`,原子写(temp+rename 0600,仿 store.go:256-270),**覆盖式**,读时坏文件容错为"无记录"。
- `Record{Verdict, VerdictLabel, EvidenceSummary, CheckedAt, Revision}`;`Revision` 取 provider 当前 revision,BaseURL/Model 变更后旧判定自动视为过期(UI 显示"配置已变更,建议重新检测")。
- 不触碰 `results/*.jsonl`、`config.json`、统计与 20k 留存机制。

## 4. engine 与并发(独立模块约束,R5)

- `Engine.CacheCheck(id)`:
  - **独立 per-provider 互斥**:`map[providerID]bool` + 自己的 `ErrCacheCheckInFlight`(HTTP 409 / TUI 冲突提示),与 probe 的 in-flight 槽(engine.go:32)完全无关;同 provider 探测进行中**不**阻塞检测(用户决策 Q10),反之亦然。
  - 不写 results、不影响健康状态;panic-recover 与 goroutine 语义仿 ProbeNow(engine.go:279-312),同步执行于调用方 goroutine(TUI 侧自行包后台 goroutine + queueDraw,actions.go:88-122 模式)。
  - disabled provider 直接放行(不检查 Enabled)。

## 5. 超时预算

- 每跳固定总超时 60s(包内常量,`context.WithTimeout`),不读取 provider 的 `TTFTTimeoutMs/TTFTSlowMs/TimeoutSec`;TTFT 仅作为证据记录。超时 → 该跳 `failed(timeout)`。
- 取值理由:2048-token prefill 正常亚秒级,60s 已极宽松;如未来做成设置再外提。

## 6. view 契约与 API

- `view.CacheCheckView{ProviderID, Verdict, VerdictLabel, Evidence, CheckedAt, Stale}` — JSON 契约 owner 是 `internal/view`(view.go:67-121 模式);`web/app.js` 渲染函数与其保持双端同步(app.js:29-33 规则,fmt_sync_test 锁法参考 review-fixpack-2)。
- 路由:`POST /api/providers/{id}/cache-check` → 200 `CacheCheckView` / 404 未知 provider(providers.go:79 模式)/ 409 检测进行中;挂 `sameOrigin` CSRF 防护(server.go:99-125)。
- EngineAPI 追加 `CacheCheck(id)` 方法 → `cmd/llm-monitor/main.go:90-100` 适配与 `server_test.go` 的 `fakeEngine` 同步补齐。

## 7. TUI

- `command.go` 注册表(init(),command.go:54-120)追加 `/cache`,零参、`needSel:true`、desc"对选中 provider 执行前缀缓存检测";不与现有命令名构成前缀冲突(command.go:50-51 不变量,`/cache` 与其余 15 条无前缀关系)。
- handler 放 `actions.go`(仿 `probeNow`):状态栏"正在检测…",后台 goroutine 调 `Engine.CacheCheck`,`queueDraw` 回主循环(run.go:218-225);用户可见文案全部进 `view/command_text.go`(command.go:32-34 规则)。
- 展示:detail 页新增常驻"前缀缓存"区块(仿 stats 块 detail.go:152-168):最近判定 + 时间 + 证据摘要 + 过期标记。

## 8. Web

- `index.html` 详情页 manual-box 内、`btn-probe` 旁加 `btn-cache-check`;结果卡片复用手动探测框样式。
- `app.js`:`cacheCheck()` fetch(带 CSRF 语义同现有 POST)、渲染函数进 init() 监听(app.js:670-707 模式);详情刷新时从 `GET /api/providers` 载荷或专用 GET 取最近判定(见开放点 §10-2)。

## 9. 测试与验证

- `internal/cachecheck`:httptest SSE mock 表驱动——命中(两跳/第二跳命中)、恒 0、剥字段、第一跳失败、第二跳失败、超时;断言 verdict 与证据字段。
- `internal/store`:覆盖式写、坏文件容错、revision 过期。
- `internal/engine`:409 重复触发、disabled 放行、不写 results(回归 A6)。
- `internal/server`:200/404/409、fakeEngine 扩展、契约形状。
- `internal/tui`:注册/needSel/执行路径(command_test.go 模式)。
- `scripts/mock-llm.py` 增加 `--cache-mode {hit,zero,strip}` 供手工烟测(TUI + Web 各过一遍)。

## 10. 开放点(实现期决定,不阻塞评审)

1. `chatMessage`/请求构造的复用方式:直接调用 probe 内部构造函数(需小重构导出)或在 cachecheck 包内自持一份——倾向前者,probe 的 buildRequest 保持单发语义不变,仅抽出可复用的纯构造部分。
2. Web 详情页读取最近判定的载荷形式:搭车 `GET /api/providers` 响应 vs 新增 `GET /api/providers/{id}/cache-check`——倾向后者,REST 语义干净,改动面也小。
3. TTFT 差分辅助证据的具体措辞(仅 unknown 时展示)。

## 11. 兼容与回滚

- 全部为新增文件 + 三处追加(命令注册表、EngineAPI、路由表),零现有行为修改;无数据迁移、无 schema 变更。
- 回滚:revert 单 commit 即可,持久化文件遗留无副作用(下次安装重写)。
