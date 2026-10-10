# LLM Monitor

本地 LLM 接口可用性监测工具。单二进制、零常驻端口:默认以终端界面(TUI)运行,按需在 TUI 内拉起浏览器面板或导出自包含 HTML 报告。

- 单请求流式文本探测,按判定规则分类(成功 / 超时 / HTTP / 连接 / 流 / 协议 / 空回复 / 中断)
- 每对象独立调度,TTFT 与总耗时统计,成功率 / 超时率 / 错误率
- 可选 usage 上报的 decode / prefill 吞吐统计
- 明细历史(JSONL 追加,每对象默认保留 20000 条)

## 快速开始

```bash
go build -o llm-monitor ./cmd/llm-monitor
./llm-monitor
```

启动即进入 TUI。按 `n` 新建第一个监测对象(填 base_url / API Key / 模型等),保存后立即开始定时探测。

## 界面与操作(TUI)

双栏布局:左侧对象列表(状态徽章),右侧所选对象的详情。

| 键 | 作用 |
|---|---|
| `↑` / `↓` | 切换对象(左栏)/ 表单内移动字段 |
| `Tab` | 左右栏焦点切换 |
| `n` / `e` / `d` | 新建 / 编辑 / 删除对象(删除有确认) |
| `p` | 手动探测当前对象 |
| `1` / `2` / `3` | 时间窗 1h / 24h / 7d |
| `R` / `S` | 版本(当前/全部)、来源(定时/手动/全部)筛选 |
| `PgDn` / `PgUp` | 明细表翻页 |
| `w` | 拉起 / 关闭浏览器面板 |
| `r` | 导出 HTML 报告 |
| `q` / `Ctrl+C` | 退出(优雅停止调度) |

表单内:`↑↓` 移动字段,`←→` 移动光标,`Enter` 前进,`Esc` 取消。

## 按需浏览器面板

TUI 内按 `w`:从 `--port`(默认 10110)起扫描空闲端口,绑定 127.0.0.1 并自动打开浏览器;再按 `w` 关闭并释放端口。仅本机访问,不设 CORS,写接口校验同源。

## 命令行

```
llm-monitor [--port N]
```

| 参数 | 说明 |
|---|---|
| `--port` | `w` 拉起面板时的起始扫描端口,默认 10110,占用时向后 +1 |

注意:进程默认**不监听任何端口**;无终端环境(nohup / 自启)不在支持范围,关闭终端即退出。

## 数据目录

| 平台 | 路径 |
|---|---|
| Linux | `~/.config/llm-monitor/` |
| macOS | `~/Library/Application Support/llm-monitor/` |
| Windows | `%AppData%\llm-monitor\` |

目录内容:

```
config.json          对象配置(原子写)
results/<id>.jsonl   每对象的探测历史(追加式,损坏行跳过并计数)
reports/             TUI 导出的 HTML 报告
```

## 直接编辑 config.json

可以在**退出程序后**手动编辑(运行中修改会在退出时被覆写)。结构:

```json
{
  "version": 1,
  "next_id": 2,
  "max_results_per_provider": 20000,
  "providers": [
    {
      "id": 1,
      "name": "示例",
      "base_url": "https://api.example.com/v1",
      "api_key": "sk-xxx",
      "model": "some-model",
      "prompt": "请回复一个词:pong",
      "max_tokens": 128,
      "timeout_sec": 60,
      "ttft_timeout_ms": 15000,
      "ttft_slow_ms": 3000,
      "interval_sec": 300,
      "enabled": true,
      "include_usage": false,
      "revision": 1,
      "created_at": 1760000000000
    }
  ]
}
```

规则:

1. **新对象 `id` 取当前 `next_id` 值,并把 `next_id` +1**——ID 永不复用,撞上已删 ID 会串历史数据。
2. 字段校验与表单一致:`base_url` 以 `http(s)://` 开头;`max_tokens > 0`;`interval_sec` 为 0(仅手动)或 ≥60;`0 < ttft_slow_ms < ttft_timeout_ms ≤ timeout_sec × 1000`;`timeout_sec > 0`。
3. 修改 `base_url` 或 `model` 时把 `revision` +1(新目标按新版本统计,旧历史隔离);其他字段改动不动 revision。
4. `prompt` 留空时按默认探测提示词处理。

## 配置字段说明

| 字段 | 说明 | 默认 |
|---|---|---|
| `name` | 对象名称(展示用) | 必填 |
| `base_url` | API 前缀,如 `https://example.com/v1` | 必填 |
| `api_key` | 明文存本地,接口与面板永不回显完整 Key | 空 |
| `model` | 模型名 | 必填 |
| `prompt` | 探测提示词 | 内置默认 |
| `max_tokens` | 每次探测的生成上限 | 128 |
| `interval_sec` | 探测间隔秒;0 = 仅手动 | 300 |
| `timeout_sec` | 总超时秒 | 60 |
| `ttft_timeout_ms` | 首内容超时毫秒 | 15000 |
| `ttft_slow_ms` | "首内容偏慢"阈值毫秒 | 3000 |
| `enabled` | 是否启用定时探测 | true |
| `include_usage` | 请求 usage 统计(吞吐指标需要) | false |

## 判定与统计口径(简)

- 结束判据:流式 `[DONE]`,或非空 `finish_reason` 后正常 EOF;否则按中断处理
- 成功但 TTFT 超过 `ttft_slow_ms` → 状态"可用,首内容偏慢",仍计成功
- 详情页统计卡与趋势图**跟随来源筛选**(定时/手动/全部):筛什么统计什么;主动取消样本永不计入
- 监测状态徽章始终由定时探测决定,不受来源筛选影响
- 无样本时比例显示"暂无样本",不显示 0%
- 吞吐仅对开启 `include_usage` 且成功的样本计算;无证据显示"—"

完整判定规则与状态优先级见 [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md)。

## HTML 报告导出

TUI 内按 `r`:以当前筛选(时间窗 / 版本 / 来源)导出到 `<数据目录>/reports/llm-monitor-<对象>-<窗口>-<时间戳>.html`。单文件自包含(内嵌样式与 SVG),离线可开、可打印;明细最多 200 行(超出注明截断);不含输出片段与 API Key。

## 开发

```bash
go test ./... -race     # 全量测试
gofmt -l .              # 格式检查
go vet ./...
```

包结构:`cmd/llm-monitor`(入口)→ `internal/tui`(默认前端)+ `internal/server`(按需面板)→ `internal/view`(共享视图组装)→ `internal/store`(存储与统计);探测在 `internal/probe` + `internal/engine`。约定见 [.trellis/spec/backend/go-conventions.md](.trellis/spec/backend/go-conventions.md)。

## License

MIT(见 [LICENSE](LICENSE))
