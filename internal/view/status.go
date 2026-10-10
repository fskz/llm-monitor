package view

// StatusText maps a final probe status (probe → store 10-status contract,
// go-conventions.md) to its Chinese label. It mirrors web/app.js STATUS_TEXT:
// the JS table stays a separate copy on the frontend, so adding or renaming a
// status means updating BOTH this switch and app.js (acceptance A6).
func StatusText(status string) string {
	switch status {
	case "ok":
		return "成功"
	case "timeout_ttft":
		return "超时（首内容）"
	case "timeout_total":
		return "超时（总）"
	case "http_error":
		return "错误（HTTP）"
	case "conn_error":
		return "错误（连接）"
	case "stream_error":
		return "错误（流）"
	case "protocol_error":
		return "错误（协议）"
	case "empty":
		return "错误（空回复）"
	case "aborted":
		return "错误（中断）"
	case "cancelled":
		return "已取消"
	}
	return status
}

// MonitorText maps a monitor status (§7.2 ladder) to its badge label. It
// mirrors the panel badge labels (STATUS_META in web/app.js).
func MonitorText(status string) string {
	switch status {
	case "ok":
		return "可用"
	case "fail":
		return "不可用"
	case "stale":
		return "结果过期"
	case "unknown":
		return "未知"
	case "disabled":
		return "已停用"
	case "manual_only":
		return "仅手动"
	}
	return status
}
