# 状态判定、调度、存储与安全研究笔记（源自 REQUIREMENTS.md §4/§5/§7/§9/§10）

## 监测状态优先级（§7.2）

按序判定，先到先得：

| # | 状态 | 条件 |
|---|------|------|
| 1 | disabled（已停用） | enabled=false |
| 2 | manual_only（仅手动） | enabled && interval_sec=0 |
| 3 | unknown（未知） | 当前 revision 无有效定时结果 |
| 4 | stale（结果过期） | now - 最近有效定时结果 finished_at > 2×interval_sec + timeout_sec |
| 5 | ok（可用） | 最新有效定时结果 status=ok；ttft_ms > ttft_slow_ms 时附加"首内容较慢"提示 |
| 6 | fail（不可用） | 最新有效定时结果为超时/错误类，展示具体原因 |

- 有效结果 = 已完成且非 cancelled 的**定时**探测；手动结果不能刷新新鲜度。
- 当前状态不由历史成功率推导（历史好但最新失败 → 不可用）。
- "正在探测"仅为附加提示，不覆盖以上状态。

## 调度规则（§7.1）

- 启动或对象启用后，enabled && interval_sec>0 的对象**立即探测一次**，再按 interval 定时。
- interval_sec=0 不自动触发（仅手动）。
- 单对象在途限制：定时+手动共享；上次未完成时**跳过**本次定时触发（不排队、不产生失败样本）。
- 不补发停机/休眠期间错过的探测。
- 停用/删除/编辑配置 → 取消在途请求（记 cancelled，不进统计），再按新配置调度。
- 20 对象 × 1 分钟间隔互不阻塞；每对象一个调度 goroutine。

## 目标版本（revision）规则

- 初始 1；PUT 修改 base_url 或 model 时 +1；其他字段修改不递增（但同样取消在途并重调度）。
- 状态与默认统计绑定当前 revision；旧版本历史可查（revision=all）。
- results 记录探测时的 base_url/model 快照。

## 配置校验（§4.1）

- `max_tokens > 0`
- `0 < ttft_slow_ms < ttft_timeout_ms ≤ timeout_sec×1000`
- `interval_sec == 0 或 ≥ 60`
- base_url 非空、http(s)://；去尾 `/` 后追加 `/chat/completions`，不自动补 `/v1`。
- name 非空。
- 页面以分/秒展示，存储用秒/毫秒（§4.1）。

## API 设计（§10）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /api/providers | 概览：配置摘要+监测状态+最近定时结果（不含完整 Key） |
| POST | /api/providers | 新增 |
| PUT | /api/providers/{id} | 更新；base_url/model 变更 → revision+1 |
| DELETE | /api/providers/{id} | 删除配置及历史，取消在途 |
| POST | /api/providers/{id}/probe | 手动探测，已有在途 → 409；同步返回最终结果 |
| GET | /api/stats?provider=&revision=&window= | 统计；revision 省略=当前，all=全部；默认仅定时 |
| GET | /api/series?provider=&revision=&window= | 趋势桶（成功比例+成功耗时+桶样本数；空桶 null） |
| GET | /api/results?provider=&revision=&window=&source=&limit=&before= | 明细分页，倒序；before 游标区分同时间戳（用 started_at+seq 复合游标）；limit 服务端上限（如 500） |
| GET | / | 内嵌面板 |

- api_key 语义：POST 未提供/空=无 Key；PUT 未提供（字段缺省）=保留原值，显式空串=null=清除（§5.4）。
- GET 返回 `api_key_set: bool` + 掩码（如前4后4，不足全掩码），不返回完整 Key。
- 手动探测同步阻塞至完成（最长 timeout_sec）；浏览器超时与服务端完成无关，服务端不重试。

## 同源防护（§5.4）

- 不开放 CORS（无 Access-Control-Allow-Origin）。
- 写操作（POST/PUT/DELETE/probe）：请求头带 Origin 且非本机地址 → 403；无 Origin（curl 等）→ 放行。浏览器跨站请求必带 Origin，可被拦截。

## 数据模型与存储（§9、§5.3）

- providers：id 自增（删除不复用）、name/base_url/api_key/model、revision、prompt/max_tokens、timeout_sec/ttft_timeout_ms/ttft_slow_ms、interval_sec、enabled、created_at（UTC ms）。
- results JSONL：provider_id/revision/base_url/model/source/started_at/finished_at/success/ttft_ms(nullable)/total_ms/status/http_status(nullable)/error/output_preview。定时探测不保存输出片段。
- ttft_ms 无文本时 null（不用 0/-1）。
- 保留上限：每对象默认 20,000 条，全局可配（max_results_per_provider）。
- 查询不全量重扫：启动加载到内存索引，追加时同步维护；stats/series 从内存计算。
- JSONL append-only；裁剪超上限时整文件重写一次（保留最新 N 条），与追加共用 per-provider 互斥锁协调，裁剪不丢新记录。
- 损坏行：加载时跳过并计数提示，不清空重建（§5.5）。
- 存储失败必须向 API/面板显式提示，不伪装成接口错误。

## Web 面板（§4.3/§11）

- 概览列表 + 详情区（趋势/明细）+ 配置表单 + 手动测试区。
- 周期刷新（仅读取，不触发探测）。
- 状态色：disabled 灰、manual_only 蓝、unknown 灰、stale 橙、ok 绿（慢=附加黄标）、fail 红；文字+颜色并用。
- 手动结果与定时统计明确区分；无样本显示"暂无样本"。
- 版本（当前/历史 revision）与时间窗（1h/24h/7d）切换。
- 图表：手写内嵌 SVG 折线，不引第三方库（§5.1 无 CDN 依赖）。

## 启动与端口（§5.2）

- 默认端口 10110，占用则 +1（尝试上限如 100 个）；--port 覆盖起始；无可用端口明确报错退出。
- 仅监听 127.0.0.1。
- 启动成功自动开浏览器（win: `cmd /c start`，linux: `xdg-open`，darwin: `open`）；失败打印实际 URL。
- 数据目录 os.UserConfigDir()/llm-monitor；配置文件权限 0600。

## Go 版本与模块

- 单模块 `cmd/llm-monitor` 入口 + `internal/` 包；go:embed 内嵌前端（web/ 目录，embed 需在与 main 同包或子目录）。
- 前端资源放 `web/`（embed.FS），主程序 embed 到二进制。
