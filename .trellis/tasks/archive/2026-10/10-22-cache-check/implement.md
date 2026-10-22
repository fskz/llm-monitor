# 执行计划:前缀缓存检测(cache-check)

对应 `prd.md`(需求/验收)与 `design.md`(技术设计)。自底向上、每步可独立验证;Trellis 流程:每步实现后跑对应验证,最终 2.2 全量检查。

## 步骤清单

- [x] **S1 判定核心** `internal/cachecheck`:Target/常量 prompt/Run/verdict 纯函数;SSE mock 表驱动单测(hit/hit-on-2nd/zero/strip/fail-1st/fail-2nd/timeout)。
  验证:`go test ./internal/cachecheck/...`
- [x] **S2 持久化** `internal/store/cachecheck.go`:`SaveCacheCheck/LoadCacheCheck`(原子写、覆盖式、坏文件容错、revision 过期)+ 单测。
  验证:`go test ./internal/store/...`
- [x] **S3 引擎编排** `engine.CacheCheck(id)`:独立互斥(ErrCacheCheckInFlight)、disabled 放行、不写 results、panic-recover + engine 单测(含"检测期间探测不阻塞"回归)。
  验证:`go test ./internal/engine/...`
- [x] **S4 契约层** `internal/view`:CacheCheckView、中文标签(command_text.go / status 标签表)、EngineAPI.CacheCheck 追加,`cmd/llm-monitor/main.go` 适配 + view 单测。
  验证:`go test ./internal/view/... ./cmd/...`
- [x] **S5 API** `internal/server`:`POST /api/providers/{id}/cache-check`(200/404/409)+ `GET .../cache-check` 最近判定;server_test 扩展 fakeEngine。
  验证:`go test ./internal/server/...`
- [x] **S6 TUI** `/cache` 注册(needSel、前缀冲突自检)+ actions handler(后台 goroutine + queueDraw)+ detail 常驻区块 + command_test。
  验证:`go test ./internal/tui/...`
- [x] **S7 Web** index.html 按钮/卡片 + app.js 渲染与监听;若新增契约函数,纳入 fmt_sync_test 锁。
  验证:`go test ./internal/view/...`(双端同步)+ 手工过一遍页面
- [x] **S8 全量检查与烟测** `go vet ./... && go test ./...`;`scripts/mock-llm.py --cache-mode` 三模式 × TUI/Web 手工烟测;对照 prd A1–A8 逐条勾验收。
  验证:`go vet ./... && go test ./...` + A1–A8 清单

## 评审门 / 回滚点

- 每步一个 commit(S1…S8),任一步失败 revert 该步即可;无数据迁移、无 schema 变更(design §11)。
- S1 完成后即可请人评审判定矩阵(verdict 纯函数)——它决定产品语义,后续步骤都是管线。
- S4–S7 涉及双端契约,合并前跑 fmt_sync_test 相关全量。

## 明确不做

- 周期巡检、批量检测、可调/自定义 prompt、assistant prefill 检测、历史曲线(prd 非目标)。
