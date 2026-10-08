# 技术设计 — LLM 接口可用性监测工具 MVP

> 需求：docs/REQUIREMENTS.md v0.2；判定口径细节见 ../research/。

## 1. 总体架构

```
cmd/llm-monitor/main.go        入口：flags、端口探测、信号处理、优雅退出
internal/probe/                单次探测：HTTP + SSE 解析 + 状态机分类（纯函数式，无全局状态）
internal/engine/               调度：每对象 goroutine、单对象在途限制、生命周期取消
internal/store/                配置 JSON + 结果 JSONL + 内存索引 + 统计计算
internal/server/               REST API、Origin 校验、静态资源挂载、状态判定
web/                           前端（package web，go:embed；原生 HTML/JS/CSS）
```

依赖方向：main → server → engine → probe；server/engine → store；server → web(仅 embed.FS)。probe 不依赖 engine/store（便于单测）。

零第三方依赖（go.mod 无 require），保证离线构建与单文件分发。

## 2. probe 包（单次探测）

```go
type Target struct {
    BaseURL, APIKey, Model, Prompt string
    MaxTokens                      int
    TTFTTimeoutMs, TotalTimeoutMs  int
}
type Outcome struct {
    Status       string // §3.3 九种之一（不含 cancelled）
    TTFTMs       *int64 // 首内容单调耗时；无有效文本为 nil
    TotalMs      int64
    HTTPStatus   *int
    ErrDetail    string // 脱敏 + 512 字符上限
    OutputPreview string // 前 200 字符
}
func Do(ctx context.Context, client *http.Client, t Target) Outcome
```

- URL：`strings.TrimRight(base_url, "/") + "/chat/completions"`。
- 请求体：`{model, messages:[{role:"user",content:prompt}], max_tokens, stream:true}`；Key 非空时加 `Authorization: Bearer`；不请求 include_usage。
- **双期限 watchdog**：入口记 `start=time.Now()`（单调）；`ttftTimer=AfterFunc(ttftDeadline, cancel)`、`totalTimer=AfterFunc(totalDeadline, cancel)`（cancel 同一 child ctx）；收到首内容后 `ttftTimer.Stop()`（race 无害：分类按时间戳判定，不按谁先回调）。parent ctx（engine 生命周期取消）同样触发 cancel，由 engine 侧覆写为 cancelled。
- **SSE 解析**：`bufio.Reader` 按 `\n` 扫描（trim `\r`），与 chunk 边界解耦。`data:` / `data: ` 均接受；`:` 开头注释行、空行跳过；`event:`/`id:`/`retry:` 行按 SSE 规范属已知字段——`event:`/`id:` 忽略内容；**其余无法识别的行（如裸 JSON 非 SSE 响应）→ protocol_error**（可解析且顶层含非空 error → stream_error）。
- 事件判定：JSON 解析失败 → protocol_error；顶层 `error` 非空 → stream_error；无 `choices` 或空（usage 事件）→ 跳过；`choices[0].delta.content` 非空 → 首内容（记单调时间戳，仅首次）；`choices[0].finish_reason` 非空 → 置结束标志。
- 结束条件：`data: [DONE]` → 终止并判 ok/empty；EOF 时已有非空 finish_reason → ok/empty；EOF 无结束证据 → aborted。
- **分类优先级**（读循环退出后统一判定，全部基于时间戳/标志，杜绝回调竞态）：
  1. 正常结束（[DONE] 或 finish_reason+EOF）→ 有文本 `ok` / 无文本 `empty`
  2. 期限触发：首内容时间戳不存在或 > ttft 期限 → `timeout_ttft`（含同时到期）；否则 → `timeout_total`（输出中途总超时，不是 aborted）
  3. 流内 error 事件 → `stream_error`；无法解析 → `protocol_error`
  4. EOF 无结束证据 → `aborted`
- 请求阶段错误：ctx.Err() 且期限已到 → 按上表超时；HTTP 非 2xx → `http_error`（读 ≤4KB body 片段）；其余传输错误（DNS/TCP/TLS）→ `conn_error`。
- 脱敏：错误文本替换 API Key 原串 → `***`；截断 512 字符。
- client：共享 `http.Client`（无 Timeout，期限由 ctx 管）；默认 Transport（尊重系统代理）；TLS 校验不跳过。

## 3. engine 包（调度）

```go
type Engine struct { ... }
func New(store ResultSink, opts ...) *Engine
func (e *Engine) Start(ctx context.Context)            // 启动全部已启用对象
func (e *Engine) Add(p Provider) / Update(p Provider, changedTarget bool) / Remove(id) / ProbeNow(id) (*Result, error) / Probing(id) bool
func (e *Engine) Shutdown(timeout time.Duration)
```

- 每对象一个调度 goroutine：立即探测一次 → `time.Ticker(interval)`；tick 时 `inFlight.CompareAndSwap` 失败即跳过（不排队、不采样）。
- 在途请求 ctx 挂在对象生命周期 ctx 下：Update（任何字段）/ Remove / 停用 / 进程退出 → cancel 在途 → 探测以 parent ctx canceled 终止 → engine 覆写 `status=cancelled` 并照常落盘（cancelled 记录持久化但不进统计）。
- `ProbeNow`（手动）：同步执行（handler goroutine 阻塞至完成，天然受 timeout 上限约束）；`inFlight` 占用失败 → 返回 ErrInFlight（API 层转 409）。手动与定时共享同一在途限制。
- 20 对象 × 1 分钟：每对象独立 goroutine + 独立 HTTP 请求，互不阻塞；probe 内部无共享锁。
- 启动时全部启用对象立即探测 → 并发 ≤ 对象数，可接受。

## 4. store 包（存储与统计）

### 4.1 布局（os.UserConfigDir()/llm-monitor）
```
config.json          {next_id, max_results_per_provider, providers:[...]}   0600
results/<id>.jsonl   每对象一个文件                                        0600
```

### 4.2 Result（JSONL 行，§9.2 + 实现新增 seq）
```go
type Result struct {
    Seq int64 `json:"seq"`               // per-provider 单调递增，追加时分配；分页游标
    ProviderID int; Revision int
    BaseURL, Model string                // 探测时快照
    Source string                        // scheduled | manual
    StartedAt, FinishedAt int64          // UTC ms
    Success bool; TTFTMs *int64; TotalMs int64
    Status string; HTTPStatus *int
    Error string `json:",omitempty"`; OutputPreview string `json:",omitempty"` // 定时探测不写 preview
}
```

### 4.3 内存索引与文件协调
- 每对象：`mu`、`records []Result`（追加序=完成序）、`nextSeq`、`latestValidScheduled *Result`（当前 revision 的最新有效定时结果，追加时增量维护；启动时倒扫一次）、`corrupt int`（加载时跳过的损坏行数）。
- 追加：`mu` 下 append 行到文件（O_APPEND）+ 内存 + 更新 latest（若满足 scheduled/非 cancelled/revision=当前）。
- 裁剪：追加后 `len>cap` → 同一 `mu` 下 temp 文件重写最新 cap 条 + `os.Rename` 覆盖（Windows 下 Go 的 Rename 使用 REPLACE_EXISTING，可覆盖）；不丢新记录。
- 加载：逐行 json.Unmarshal，损坏行跳过计数（不清空不重写原文件）；超过 cap 的尾部多余记录加载后在首次追加时触发裁剪。
- 查询（stats/series/results）均在单对象内存 slice 上线性/排序扫描（≤20k 条，微秒级）；概览接口只读各对象 latest 指针，**不扫历史**——满足"刷新不重复全量扫描"。
- 存储错误：Append 失败向上返回 → API 500 + 面板提示，绝不伪装成探测失败。

### 4.4 统计
- 分母：`source=scheduled && status!=cancelled`，按 `started_at >= now-window` 过滤；revision 省略=当前，`all`=全部。
- 输出：samples、ok/timeout(timeout_ttft/timeout_total)/error 计数与百分比（samples=0 → 全部 null）。
- series 桶宽：1h→12×5min；24h→24×1h；7d→28×6h。每桶：start_ms、samples、ok_pct、avg_ttft_ms、avg_total_ms（仅 ok 样本；空桶 null）。

## 5. server 包（API + 状态判定）

- 路由按 §10 表；JSON 错误体 `{"error":"..."}`。
- **状态判定**（概览接口逐对象计算）：
  1. `!enabled` → disabled；2. `interval=0` → manual_only；3. 无当前 revision 有效定时结果 → unknown；4. `now-finished_at > 2*interval+timeout` → stale；5. latest.status=ok → ok（`ttft_ms>ttft_slow_ms` 附 slow 标志）；6. 否则 fail + 原因文本。
  - 附 `probing` 布尔（正在探测提示，不改变上述状态）。
- Key 掩码：`api_key_set` + 前 4 后 4（长度 ≤8 全 `*`）。
- PUT：`api_key` 为 `*string`——缺省(nil)=保留、`""`=清除、非空=替换；base_url(去尾/后)或 model 变化 → revision+1。
- Origin 校验中间件（POST/PUT/DELETE/probe）：带 Origin 且 host 非本机监听地址 → 403；无 Origin 放行（curl）；响应不带 CORS 头。
- results 分页：`(started_at,seq)` 复合游标（base64），倒序，limit 默认 50 上限 500。
- 静态：`web.FS`（embed）挂 `/`。

## 6. main / 启动

- flags：`-port`（默认 10110）。监听探测：`net.Listen("tcp","127.0.0.1:p")` 从起始端口尝试 +1，上限 100 次，全失败报错退出。
- 监听成功后 goroutine 开浏览器（win `cmd /c start`、linux `xdg-open`、darwin `open`），无论成败 stdout 打印实际 URL。
- 信号 SIGINT/SIGTERM：engine.Shutdown（停调度 + cancel 在途→cancelled 落盘，WaitGroup 等 ≤10s）→ store 关闭 → 退出。

## 7. 前端（web/，中文 UI）

- `index.html` + `app.js` + `style.css`（原生，无框架无 CDN）。
- 布局：概览表（状态徽章[文字+色]、最近结果时间、三率、样本数、操作按钮）→ 选中对象详情（时间窗/版本切换、两张 SVG 趋势图[成功率、成功耗时 avg ttft/total]、明细表[筛选 source/状态]、手动测试区）→ 配置弹窗（新增/编辑，含"清除 Key"独立勾选以满足"空=保留"语义）。
- 概览 5s 轮询（仅 GET）；状态色：disabled/unknown 灰、manual_only 蓝、stale 橙、ok 绿（slow 加黄点）、fail 红。
- 图表手写 SVG 折线；空桶断开（null 不插值）。

## 8. 关键权衡记录

| 决策 | 备选 | 取舍 |
|------|------|------|
| 内存索引 + 启动全量加载 | 每次查询扫文件 | 400k 条上限（20×20k）内存可控（每条 <1KB，最坏 ~400MB 偏大——实际条目 ~300B，~120MB；MVP 接受，SQLite 为 M4 后备） |
| watchdog 双定时器 + 事后按时间戳分类 | ctx 双 deadline | Go ctx 期限只能缩短不能延长；时间戳判定消除竞态 |
| cancelled 也落盘 | 不落盘 | §12.1-6 明确测试数据含取消记录且需可从统计中排除 |
| 手写 SVG | 内嵌图表库 | 体积与零依赖优先 |
| results 文件按对象分拆 | 单文件 | 删除对象=删文件；裁剪/追加竞争域缩小 |

## 9. 兼容 / 回滚

- 绿地项目无迁移负担。数据文件版本字段预留（config.json `version:1`），损坏时跳行不重建。
- 回滚 = 整体回退 commit；数据文件向后兼容（新增字段 JSON 忽略未知键）。

## 10. 测试设计（映射验收 §12）

- probe 单测（httptest 模拟）：A1.1–A1.5 全场景矩阵（正常流/角色先行/延迟文本/双超时/401/429/500/200 错误事件/垃圾行/中途断开/finish_reason EOF/empty/max_tokens=length）。
- store 单测：追加/裁剪/损坏行/重启一致性/游标分页（A2.3）。
- engine 单测：在途跳过、编辑取消、不补发（A2.1）。
- server 单测：状态六态矩阵（A1.7）、Key 掩码、Origin 403（A2.6）、A1.6 统计比例精确断言。
- 构建：Makefile 交叉编译 windows/linux/darwin（amd64+arm64），产出 .exe（A2.4 的产物部分；真机 Windows 手测留给用户）。
