# 实施计划 — LLM 接口可用性监测工具 MVP

> 前置：prd.md（验收 A1/A2）、design.md（架构）、research/（判定口径）。
> 执行方式：主会话逐层实现（trellis-implement 子代理逐块分发），每步自带验证命令。

## 实施顺序（自底向上，每步可独立验证）

### S1 项目骨架
- [ ] go.mod（module llm-monitor，Go ≥1.22，零依赖）
- [ ] cmd/llm-monitor/main.go：flags + 端口探测（10110 起 +1，上限 100 次）+ 信号处理骨架
- [ ] 验证：`go build ./... && go vet ./...`

### S2 probe 包（判定核心）
- [ ] Target/Outcome 类型；`Do(ctx, client, Target) Outcome`
- [ ] 双期限 watchdog（ttft/total 双 timer，时间戳判定）
- [ ] SSE 行解析器（\n 扫描、data:/data: 、注释、[DONE]、usage 跳过、首内容/finish_reason 标志）
- [ ] 分类决策树（ok/empty/timeout_ttft/timeout_total/stream_error/protocol_error/aborted/http_error/conn_error）
- [ ] 错误脱敏（Key 替换 ***、512 截断）、output_preview 截 200 字符
- 验证：`go test ./internal/probe/ -race -count=1`（httptest 模拟 §12.1 场景矩阵：A1.1-A1.5）

### S3 store 包
- [ ] config.json 读写（0600、next_id、max_results_per_provider）
- [ ] results/<id>.jsonl：加载（跳损坏行计数）/追加/裁剪（temp+rename，per-provider 锁）
- [ ] 内存索引：records、nextSeq、latestValidScheduled（增量维护 + 启动倒扫）
- [ ] Stats/Series/QueryResults（时间窗、revision=current/all、source 筛选、游标分页）
- 验证：`go test ./internal/store/ -race -count=1`（追加/裁剪/损坏行/重启一致性/分页/A1.6 比例断言）

### S4 engine 包
- [ ] 每对象调度 goroutine（立即一次 + Ticker；inFlight CAS 跳过）
- [ ] Add/Update/Remove/ProbeNow/Probing；Update/Remove/停用取消在途→cancelled 落盘
- [ ] Shutdown（停调度、取消在途、等 ≤10s）
- 验证：`go test ./internal/engine/ -race -count=1`（在途跳过/编辑取消/不补发 A2.1）

### S5 server 包
- [ ] 路由 §10 全表 + JSON 错误体
- [ ] 状态六态判定（§7.2 优先级）+ probing 附加位
- [ ] Key 掩码（api_key_set + 前4后4）；PUT api_key *string 语义（nil=保留/""=清除/非空=替换）；revision 递增
- [ ] Origin 校验中间件（写操作带 Origin 且非本机→403；无 CORS 头）
- [ ] results 复合游标分页（(started_at,seq) base64）
- 验证：`go test ./internal/server/ -race -count=1`（六态矩阵/掩码/Origin 403/409/A1.6 端到端）

### S6 web 前端
- [ ] index.html + app.js + style.css（原生，无 CDN）
- [ ] 概览表（状态徽章文字+色、三率、样本数、"暂无样本"）；5s 轮询
- [ ] 详情区（时间窗/版本切换、SVG 成功率与耗时趋势[空桶断开]、明细筛选）
- [ ] 配置弹窗（校验规则前端提示、Key 掩码/清除勾选）、删除确认、手动测试区（结果+TTFT+总耗时+preview、"正在探测"提示）
- 验证：`go build ./...`；构建后手动冒烟（浏览器或 curl /api/*）

### S7 集成与构建
- [ ] main.go 串联 store→engine→server；自动开浏览器（三平台命令）+ URL 打印；优雅退出
- [ ] Makefile：build（本机）、release（windows/linux/darwin × amd64/arm64，含 .exe）
- [ ] 端到端冒烟：启动二进制 → curl 建对象（指向 httptest 式 mock 或跳过）→ 查概览/统计
- 验证：`go build ./... && go vet ./... && go test ./... -race -count=1 && make build`

### S8 质量检查（trellis-check）
- [ ] 全量测试 + race + vet；对照 prd.md A1/A2 逐条自查
- [ ] 冒烟：`make build && ./llm-monitor-bin &` → curl 探测接口
- [ ] 更新 .trellis/spec/backend（Go 项目约定沉淀）

## 风险点 / 回滚

| 风险 | 缓解 |
|------|------|
| SSE 解析边界（chunk/行不对齐） | S2 行缓冲解析器 + 模拟场景矩阵覆盖 |
| Windows Rename 覆盖 | Go os.Rename Windows 语义 REPLACE_EXISTING；裁剪在锁内 |
| 内存上限（20×20k 条） | 条目 ~300B；MVP 接受，design §8 记录 SQLite 后备 |
| 双 timer 竞态 | 分类只按时间戳，不按回调顺序 |

回滚点：每步一个 commit（S1-S7），损坏则 revert 对应步骤。

## 完成定义

prd.md A1（8 条）+ A2（7 条）全部可验证通过（A2.4 真机 Windows 部分手测除外，交付构建产物与说明）。
