/* global state */
let providers = [];          // GET /api/providers
let currentId = null;        // selected provider id
let curRevision = "";        // "" = current revision; "all" or number string
let curWindow = "24h";
let curSource = "scheduled";
let appSettings = null;   // GET /api/settings cache (10-10-settings-pack)
let pageCursor = null;       // cursor used to fetch the current page (null = first)
let nextCursor = null;       // next_cursor from the latest response (null = no more)
let prevCursors = [];        // stack of cursors-to-use for going back
let overviewTimer = null;

const $ = (id) => document.getElementById(id);

const STATUS_META = {
  ok:          { text: "可用",           cls: "ok" },
  fail:        { text: "不可用",         cls: "fail" },
  stale:       { text: "结果过期",       cls: "stale" },
  unknown:     { text: "未知",           cls: "unknown" },
  disabled:    { text: "已停用",         cls: "disabled" },
  manual_only: { text: "仅手动",         cls: "manual_only" },
};
const STATUS_TEXT = {
  ok: "成功", timeout_ttft: "超时（首内容）", timeout_total: "超时（总）",
  http_error: "错误（HTTP）", conn_error: "错误（连接）", stream_error: "错误（流）",
  protocol_error: "错误（协议）", empty: "错误（空回复）", aborted: "错误（中断）",
  cancelled: "已取消",
};

/* ---------- helpers ---------- */

async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const j = await res.json();
      if (j.error) msg = j.error;
    } catch { /* ignore */ }
    const err = new Error(msg);
    err.status = res.status;
    throw err;
  }
  return res.json();
}

function fmtPct(v) { return v == null ? "—" : v.toFixed(2) + "%"; }

function fmtMs(v) {
  if (v == null) return "—";
  if (v < 1000) return v + " ms";
  return (v / 1000).toFixed(2) + " s";
}

function fmtTPS(v) { return v == null ? "—" : v.toFixed(1) + " tok/s"; }
// TPOT is the reciprocal view of decode TPS over the same evidence
// (usage-enabled ok samples): ms per output token.
function fmtTPOT(decodeTps) { return decodeTps == null ? "—" : (1000 / decodeTps).toFixed(1) + " ms/tok"; }
// Flip ("jitter") rate with a qualitative band: <5% 稳定, 5-20% 波动, >20% 频繁翻转.
function fmtFlip(r) {
  if (r == null) return "—";
  const pct = (r * 100).toFixed(1) + "%";
  const band = r < 0.05 ? "稳定" : r <= 0.2 ? "波动" : "频繁翻转";
  return `${pct}（${band}）`;
}

function fmtTime(ms) {
  if (!ms) return "—";
  const d = new Date(ms);
  const pad = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function statusBadge(p) {
  const meta = STATUS_META[p.status] || STATUS_META.unknown;
  let html = `<span class="badge ${meta.cls}">${meta.text}`;
  if (p.status === "ok" && p.slow_ttft) html += `<span class="slow-dot" title="可用，但首内容较慢"></span>`;
  if (p.probing) html += `<span class="muted"> · 正在探测</span>`;
  html += `</span>`;
  // Consecutive-failure badge: threshold from settings (default 2) makes
  // "真挂了" visible next to the single-result status.
  const threshold = (appSettings && appSettings.streak_alert) || 2;
  if ((p.streak_fail || 0) >= threshold) html += ` <span class="badge fail" title="连续失败 ${p.streak_fail} 次">连败 ${p.streak_fail}</span>`;
  return html;
}

// Probing hint must not override disabled/unknown/stale display; it is appended
// inside the badge text only (requirement §7.2), which statusBadge already does.

/* ---------- overview ---------- */

async function loadOverview() {
  try {
    providers = await api("/api/providers");
    if (!appSettings) appSettings = await api("/api/settings"); // once; the dialog refreshes it
    $("storage-warn").classList.toggle("hidden", !providers.some((p) => p.storage_error));
    renderOverview();
  } catch (e) {
    console.error("load overview failed:", e);
  }
}

function renderOverview() {
  const wrap = $("provider-list");
  if (!providers.length) {
    wrap.innerHTML = `<p class="empty-hint">暂无监测对象，点击右上角添加。</p>`;
    return;
  }
  wrap.innerHTML = providers.map((p) => {
    const st = p.stats || {};
    const last = p.last_probe;
    // Failure detail (§7.2-6): the unavailable state must show the concrete
    // reason. All server-provided strings pass through esc().
    const reason = last && last.status !== "ok" && last.error
      ? `：${last.error}` : "";
    const lastTxt = last
      ? `${esc(STATUS_TEXT[last.status] || last.status)}${esc(reason)} · ${fmtTime(last.finished_at)}`
      : "尚无定时探测";
    const threshold = (appSettings && appSettings.streak_alert) || 2;
    const alerting = (p.streak_fail || 0) >= threshold ? " alerting" : "";
    return `<div class="card${alerting}" data-id="${p.id}">
      <div class="card-top">
        <div><h3>${esc(p.name)}</h3><div class="model">${esc(p.model)}</div></div>
        ${statusBadge(p)}
      </div>
      <div class="card-rates">
        <div class="rate rate-ok">成功率 <b>${fmtPct(st.ok_pct)}</b> <span class="num">${st.ok ?? 0}/${st.samples ?? 0}</span></div>
        <div class="rate rate-timeout">超时率 <b>${fmtPct(st.timeout_pct)}</b></div>
        <div class="rate rate-error">错误率 <b>${fmtPct(st.error_pct)}</b></div>
      </div>
      <div class="card-foot">
        <span class="muted last-line">${lastTxt}</span>
        <span class="actions">
          <button class="ghost act-toggle" data-id="${p.id}" title="${p.enabled ? "停止自动定时探测（手动测试仍可用）" : "恢复自动定时探测"}">${p.enabled ? "停用" : "启用"}</button>
          <button class="ghost act-clone" data-id="${p.id}" title="复制全部配置（含密钥）为新的监测对象，克隆后可直接改模型名">克隆</button>
          <button class="ghost act-edit" data-id="${p.id}">编辑</button>
          <button class="ghost act-delete" data-id="${p.id}">删除</button>
        </span>
      </div>
    </div>`;
  }).join("");

  wrap.querySelectorAll(".card").forEach((el) => {
    el.addEventListener("click", (ev) => {
      if (ev.target.closest("button")) return;
      openDetail(Number(el.dataset.id));
    });
  });
  wrap.querySelectorAll(".act-edit").forEach((b) =>
    b.addEventListener("click", (ev) => { ev.stopPropagation(); openProviderDialog(Number(b.dataset.id)); }));
  wrap.querySelectorAll(".act-toggle").forEach((b) =>
    b.addEventListener("click", (ev) => { ev.stopPropagation(); toggleProvider(Number(b.dataset.id)); }));
  wrap.querySelectorAll(".act-clone").forEach((b) =>
    b.addEventListener("click", (ev) => { ev.stopPropagation(); cloneProvider(Number(b.dataset.id)); }));
  wrap.querySelectorAll(".act-delete").forEach((b) =>
    b.addEventListener("click", (ev) => { ev.stopPropagation(); confirmDelete(Number(b.dataset.id)); }));
}

/* ---------- detail ---------- */

function windowMs() {
  return { "1h": 3600e3, "24h": 86400e3, "7d": 7 * 86400e3 }[curWindow];
}

function revisionParam() {
  if (curRevision === "" || curRevision === "all") return curRevision;
  return String(curRevision);
}

async function openDetail(id) {
  currentId = id;
  curRevision = "";
  pageCursor = null; nextCursor = null; prevCursors = [];
  $("overview").classList.add("hidden");
  $("detail").classList.remove("hidden");
  const p = providers.find((x) => x.id === id);
  $("detail-title").textContent = p ? `${p.name} · ${p.model}` : `#${id}`;
  await refreshDetail();
}

async function refreshDetail() {
  if (currentId == null) return;
  const rev = revisionParam();
  const qs = `provider=${currentId}&revision=${encodeURIComponent(rev)}&window=${curWindow}`;
  const src = `&source=${curSource}`; // stats/series follow the source filter (10-10)
  const [providersAll, stats, series, results] = await Promise.all([
    api("/api/providers"),
    api(`/api/stats?${qs}${src}`),
    api(`/api/series?${qs}${src}`),
    api(`/api/results?${qs}${src}&limit=50${pageCursor ? `&before=${encodeURIComponent(pageCursor)}` : ""}`),
  ]);
  providers = providersAll;
  const p = providers.find((x) => x.id === currentId);
  if (!p) { closeDetail(); return; }
  $("detail-status").innerHTML = statusBadge(p);
  renderRevisions(p);
  renderStats(stats);
  renderCharts(series);
  renderResults(results, stats);
}

function renderRevisions(p) {
  const sel = $("sel-revision");
  const current = String(p.revision);
  const keep = curRevision;
  let html = `<option value="">当前（v${current}）</option>`;
  if (p.revisions && p.revisions.length > 1) {
    for (const r of p.revisions) {
      if (String(r) !== current) html += `<option value="${r}">v${r}</option>`;
    }
    html += `<option value="all">全部版本</option>`;
  }
  sel.innerHTML = html;
  sel.value = keep;
  if (sel.value !== keep) { sel.value = ""; curRevision = ""; }
}

function renderStats(stats) {
  const row = $("stat-row");
  const items = [
    { l: "样本数", v: stats.samples ?? 0 },
    { l: "成功", v: `${stats.ok ?? 0}（${fmtPct(stats.ok_pct)}）`, cls: "rate-ok" },
    { l: "超时", v: `${stats.timeout ?? 0}（${fmtPct(stats.timeout_pct)}）`, cls: "rate-timeout" },
    { l: "错误", v: `${stats.error ?? 0}（${fmtPct(stats.error_pct)}）`, cls: "rate-error" },
    { l: "平均 TTFT（成功）", v: fmtMs(stats.avg_ttft_ms) },
    { l: "TTFT P50 / P95（成功）", v: `${fmtMs(stats.ttft_p50_ms)} / ${fmtMs(stats.ttft_p95_ms)}` },
    { l: "总耗时 P50 / P95（成功）", v: `${fmtMs(stats.total_p50_ms)} / ${fmtMs(stats.total_p95_ms)}` },
    { l: "抖动（结果翻转率）", v: fmtFlip(stats.flip_rate) },
    { l: "平均总耗时（成功）", v: fmtMs(stats.avg_total_ms) },
    { l: "平均 decode 吞吐（成功）", v: fmtTPS(stats.avg_decode_tps) },
    { l: "平均 TPOT（成功）", v: fmtTPOT(stats.avg_decode_tps) },
    { l: "平均 prefill 吞吐（成功，近似·含排队）", v: fmtTPS(stats.avg_prefill_tps) },
  ];
  const breakdown = (stats.errors_by_kind || []).map((e) => `${e.label}×${e.count}`).join(" · ");
  if (breakdown) items.push({ l: "错误分布", v: breakdown, wide: true });
  row.innerHTML = items.map((it) =>
    `<div class="stat${it.wide ? " wide" : ""}"><div class="v ${it.cls || ""}">${esc(String(it.v))}</div><div class="l">${it.l}</div></div>`).join("");
  $("detail-samples").textContent = stats.samples ? `统计样本 ${stats.samples} 条` : "暂无样本";
}

/* ---------- charts (hand-written SVG) ---------- */

function bucketPath(buckets, key, x, y) {
  let d = "";
  let pen = false;
  buckets.forEach((b, i) => {
    const v = b[key];
    if (v == null) { pen = false; return; }
    const px = x(i), py = y(v);
    d += `${pen ? "L" : "M"}${px.toFixed(1)},${py.toFixed(1)} `;
    pen = true;
  });
  return d.trim();
}

function renderCharts(series) {
  const buckets = series.buckets || [];
  const hasData = buckets.some((b) => b.samples > 0);
  if (!hasData) {
    $("chart-ok").innerHTML = `<div class="chart-noData">暂无样本</div>`;
    $("chart-latency").innerHTML = `<div class="chart-noData">暂无样本</div>`;
    return;
  }
  const W = 480, H = 140, PL = 34, PR = 8, PT = 8, PB = 20;
  const iw = W - PL - PR, ih = H - PT - PB;
  const n = Math.max(buckets.length, 1);
  const x = (i) => PL + (i + 0.5) * (iw / n);

  // success-rate chart: 0-100%
  const yPct = (v) => PT + ih - (v / 100) * ih;
  const gridPct = [0, 50, 100].map((v) =>
    `<line class="gridline" x1="${PL}" y1="${yPct(v)}" x2="${W - PR}" y2="${yPct(v)}"/>
     <text x="${PL - 4}" y="${yPct(v) + 3}" text-anchor="end">${v}%</text>`).join("");
  const labels = xLabels(buckets, PL, W, PR);
  const okPath = bucketPath(buckets, "ok_pct", x, yPct);
  const okDots = buckets.map((b, i) => b.ok_pct == null ? "" :
    `<circle cx="${x(i)}" cy="${yPct(b.ok_pct)}" r="2" fill="var(--ok)"/>`).join("");
  $("chart-ok").innerHTML =
    `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">${gridPct}${labels}
      <path class="line-ok" d="${okPath}"/>${okDots}</svg>`;

  // latency chart: scale to max avg_total
  const lat = buckets.filter((b) => b.avg_total_ms != null);
  if (!lat.length) {
    $("chart-latency").innerHTML = `<div class="chart-noData">暂无成功样本</div>`;
    return;
  }
  const maxV = Math.max(...lat.map((b) => b.avg_total_ms), 1000);
  const yMs = (v) => PT + ih - (v / maxV) * ih;
  const steps = 2;
  const gridMs = Array.from({ length: steps + 1 }, (_, k) => {
    const v = (maxV / steps) * k;
    return `<line class="gridline" x1="${PL}" y1="${yMs(v)}" x2="${W - PR}" y2="${yMs(v)}"/>
      <text x="${PL - 4}" y="${yMs(v) + 3}" text-anchor="end">${fmtMs(v)}</text>`;
  }).join("");
  const ttftPath = bucketPath(buckets, "avg_ttft_ms", x, yMs);
  const totalPath = bucketPath(buckets, "avg_total_ms", x, yMs);
  $("chart-latency").innerHTML =
    `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">${gridMs}${labels}
      <path class="line-total" d="${totalPath}"/>
      <path class="line-ttft" d="${ttftPath}"/>
      <text class="legend" x="${W - PR}" y="${H - 4}" text-anchor="end">
        <tspan fill="var(--manual)">— 总耗时</tspan>
        <tspan dx="8" fill="var(--primary)">— TTFT</tspan>
      </text></svg>`;
}

function xLabels(buckets, PL, W, PR) {
  const first = buckets[0], last = buckets[buckets.length - 1];
  if (!first || !last) return "";
  const fmt = (ms) => {
    const d = new Date(ms);
    const pad = (n) => String(n).padStart(2, "0");
    return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
  };
  return `<text x="${PL}" y="132">${fmt(first.start_ms)}</text>
    <text x="${W - PR}" y="132" text-anchor="end">${fmt(last.start_ms)}</text>`;
}

/* ---------- results table ---------- */

function renderResults(page, stats) {
  const body = $("results-body");
  if (!page.items || !page.items.length) {
    body.innerHTML = `<tr><td colspan="8" class="muted" style="text-align:center;padding:24px">暂无记录</td></tr>`;
  } else {
    body.innerHTML = page.items.map((r) => {
      const cls = r.status === "ok" ? "status-ok"
        : r.status.startsWith("timeout") ? "status-timeout"
        : r.status === "cancelled" ? "" : "status-fail";
      const err = r.error ? `<span class="muted">${esc(r.error)}</span>` : "";
      const note = r.status === "cancelled" ? "主动取消，不计入统计" : (STATUS_TEXT[r.status] || r.status);
      return `<tr class="${cls}">
        <td>${fmtTime(r.started_at)}</td>
        <td>${r.source === "manual" ? "手动" : "定时"}</td>
        <td class="status-cell">${r.success ? "✔" : "✘"}</td>
        <td class="status-cell">${note}</td>
        <td>${fmtMs(r.ttft_ms)}</td>
        <td>${fmtMs(r.total_ms)}</td>
        <td>${r.http_status ?? "—"}</td>
        <td class="wrap">${err}</td>
      </tr>`;
    }).join("");
  }
  nextCursor = page.next_cursor || null;
  $("btn-next").disabled = !nextCursor;
  $("btn-prev").disabled = prevCursors.length === 0;
}

/* ---------- provider dialog ---------- */

function defaultPrompt() { return "请回复： pong"; }

function openProviderDialog(id) {
  const dlg = $("dlg-provider");
  $("form-error").classList.add("hidden");
  $("f-clearkey").checked = false;
  $("f-clearkey").parentElement.classList.add("hidden");
  const p = id != null ? providers.find((x) => x.id === id) : null;
  $("dlg-title").textContent = p ? `编辑：${p.name}` : "添加监测对象";
  $("f-id").value = p ? p.id : "";
  $("f-name").value = p ? p.name : "";
  $("f-baseurl").value = p ? p.base_url : "";
  $("f-apikey").value = "";
  $("f-apikey-hint").textContent = p
    ? (p.api_key_set ? `已配置（${p.api_key_mask}），留空保持不变` : "未配置")
    : "可留空（无需鉴权的自托管接口）";
  if (p && p.api_key_set) $("f-clearkey").parentElement.classList.remove("hidden");
  $("f-model").value = p ? p.model : "";
  $("f-prompt").value = p ? p.prompt : defaultPrompt();
  // New-provider defaults come from the tool settings when loaded
  // (10-10-settings-pack); editing prefills the object itself.
  const d = (appSettings && appSettings.defaults) || { max_tokens: 128, interval_sec: 300, timeout_sec: 60, ttft_timeout_ms: 15000, ttft_slow_ms: 3000, enabled: true, include_usage: false };
  $("f-maxtokens").value = p ? p.max_tokens : d.max_tokens;
  $("f-interval").value = p ? (p.interval_sec ? p.interval_sec / 60 : 0) : (d.interval_sec ? d.interval_sec / 60 : 0);
  $("f-ttft").value = p ? p.ttft_timeout_ms / 1000 : d.ttft_timeout_ms / 1000;
  $("f-timeout").value = p ? p.timeout_sec : d.timeout_sec;
  $("f-slow").value = p ? p.ttft_slow_ms / 1000 : d.ttft_slow_ms / 1000;
  $("f-enabled").checked = p ? p.enabled : d.enabled;
  $("f-usage").checked = p ? !!p.include_usage : !!d.include_usage;
  dlg.showModal();
}

function readProviderForm() {
  const intervalMin = Number($("f-interval").value || 0);
  return {
    name: $("f-name").value.trim(),
    base_url: $("f-baseurl").value.trim(),
    model: $("f-model").value.trim(),
    prompt: $("f-prompt").value.trim() || defaultPrompt(),
    max_tokens: Number($("f-maxtokens").value || 0),
    interval_sec: Math.round(intervalMin * 60),
    timeout_sec: Number($("f-timeout").value || 0),
    ttft_timeout_ms: Math.round(Number($("f-ttft").value || 0) * 1000),
    ttft_slow_ms: Math.round(Number($("f-slow").value || 0) * 1000),
    enabled: $("f-enabled").checked,
    include_usage: $("f-usage").checked,
  };
}

function validateProvider(c) {
  if (!c.name) return "名称不能为空";
  if (!/^https?:\/\//.test(c.base_url)) return "Base URL 必须以 http:// 或 https:// 开头（API 前缀，如 https://example.com/v1）";
  if (!c.model) return "模型不能为空";
  if (!(c.max_tokens > 0)) return "max_tokens 必须大于 0";
  if (!(c.interval_sec === 0 || c.interval_sec >= 60)) return "探测间隔为 0（仅手动）或至少 1 分钟";
  if (!(c.ttft_slow_ms > 0 && c.ttft_slow_ms < c.ttft_timeout_ms)) return "需要 0 < 慢阈值 < 首内容超时";
  if (!(c.ttft_timeout_ms <= c.timeout_sec * 1000)) return "首内容超时不能大于总超时";
  if (!(c.timeout_sec > 0)) return "总超时必须大于 0";
  return null;
}

async function saveProvider(ev) {
  ev.preventDefault();
  const cfg = readProviderForm();
  const verr = validateProvider(cfg);
  if (verr) { showFormError(verr); return; }
  const id = $("f-id").value;
  // api_key semantics: absent = keep, "" = clear (send only when user typed or checked clear)
  if ($("f-clearkey").checked && !$("f-apikey").value) cfg.api_key = "";
  else if ($("f-apikey").value) cfg.api_key = $("f-apikey").value;
  try {
    if (id) {
      await api(`/api/providers/${id}`, { method: "PUT", body: JSON.stringify(cfg) });
    } else {
      await api("/api/providers", { method: "POST", body: JSON.stringify(cfg) });
    }
    $("dlg-provider").close();
    await loadOverview();
  } catch (e) {
    showFormError(e.message);
  }
}

function showFormError(msg) {
  const el = $("form-error");
  el.textContent = msg;
  el.classList.remove("hidden");
}

/* ---------- settings dialog (10-10-settings-pack) ---------- */

function openSettingsDialog() {
  const dlg = $("dlg-settings");
  $("settings-error").classList.add("hidden");
  api("/api/settings").then((s) => {
    appSettings = s;
    $("s-maxtokens").value = s.defaults.max_tokens;
    $("s-interval").value = s.defaults.interval_sec ? s.defaults.interval_sec / 60 : 0;
    $("s-ttft").value = s.defaults.ttft_timeout_ms / 1000;
    $("s-timeout").value = s.defaults.timeout_sec;
    $("s-slow").value = s.defaults.ttft_slow_ms / 1000;
    $("s-enabled").checked = s.defaults.enabled;
    $("s-usage").checked = !!s.defaults.include_usage;
    $("s-streak").value = s.streak_alert;
    $("s-reportdir").value = s.report_dir || "";
    dlg.showModal();
  }).catch((e) => alert("读取设置失败：" + e.message));
}

async function saveSettings(ev) {
  ev.preventDefault();
  const body = {
    defaults: {
      max_tokens: Number($("s-maxtokens").value),
      interval_sec: Math.round(Number($("s-interval").value) * 60),
      timeout_sec: Number($("s-timeout").value),
      ttft_timeout_ms: Math.round(Number($("s-ttft").value) * 1000),
      ttft_slow_ms: Math.round(Number($("s-slow").value) * 1000),
      enabled: $("s-enabled").checked,
      include_usage: $("s-usage").checked,
    },
    streak_alert: Number($("s-streak").value),
    report_dir: $("s-reportdir").value.trim(),
  };
  try {
    appSettings = await api("/api/settings", { method: "PUT", body: JSON.stringify(body) });
    $("dlg-settings").close();
    await loadOverview(); // thresholds/badges re-render immediately
  } catch (e) {
    const el = $("settings-error");
    el.textContent = e.message;
    el.classList.remove("hidden");
  }
}

// toggleProvider flips the enabled flag through the regular PUT: the
// engine's Update cancels any in-flight probe and stops/resumes the
// scheduler; manual probes stay available while disabled. api_key is
// omitted so the stored key is kept (§5.4 absent = keep).
async function toggleProvider(id) {
  const p = providers.find((x) => x.id === id);
  if (!p) return;
  const body = {
    name: p.name, base_url: p.base_url, model: p.model,
    prompt: p.prompt, max_tokens: p.max_tokens, interval_sec: p.interval_sec,
    timeout_sec: p.timeout_sec, ttft_timeout_ms: p.ttft_timeout_ms,
    ttft_slow_ms: p.ttft_slow_ms, include_usage: !!p.include_usage,
    enabled: !p.enabled,
  };
  try {
    await api(`/api/providers/${id}`, { method: "PUT", body: JSON.stringify(body) });
    await loadOverview();
  } catch (e) {
    alert((p.enabled ? "停用" : "启用") + "失败：" + e.message);
  }
}

// cloneProvider duplicates a provider server-side (the API key is copied
// there — it never reaches this page) and opens the edit dialog on the
// new copy with the model field focused: the clone-for-model-variants
// flow is click → type model name → save.
async function cloneProvider(id) {
  let created;
  try {
    created = await api(`/api/providers/${id}/clone`, { method: "POST" });
  } catch (e) {
    alert("克隆失败：" + e.message);
    return;
  }
  await loadOverview();
  openProviderDialog(created.id);
  const model = $("f-model");
  model.focus();
  model.select();
}

async function confirmDelete(id) {
  const p = providers.find((x) => x.id === id);
  $("delete-msg").textContent = `确定删除「${p ? p.name : id}」？配置与全部历史记录将一并删除。`;
  const dlg = $("dlg-delete");
  dlg.onclose = async () => {
    if (dlg.returnValue === "ok") {
      try { await api(`/api/providers/${id}`, { method: "DELETE" }); await loadOverview(); }
      catch (e) { alert("删除失败：" + e.message); }
    }
    dlg.onclose = null;
  };
  dlg.showModal();
}

/* ---------- manual probe ---------- */

async function manualProbe() {
  const btn = $("btn-probe");
  const box = $("manual-result");
  btn.disabled = true; btn.textContent = "正在探测…";
  box.classList.add("hidden");
  try {
    const r = await api(`/api/providers/${currentId}/probe`, { method: "POST" });
    const cls = r.status === "ok" ? "badge ok" : "badge fail";
    box.innerHTML = `
      <table>
        <tr><td>结果</td><td><span class="${cls}">${STATUS_TEXT[r.status] || r.status}</span>
          ${r.status === "ok" && r.slow ? `<span class="muted">（首内容较慢）</span>` : ""}</td></tr>
        <tr><td>TTFT</td><td>${fmtMs(r.ttft_ms)}</td></tr>
        <tr><td>总耗时</td><td>${fmtMs(r.total_ms)}</td></tr>
        ${r.prompt_tokens != null || r.completion_tokens != null ? `<tr><td>Token 数</td><td>prompt ${r.prompt_tokens ?? "—"} / completion ${r.completion_tokens ?? "—"}</td></tr>` : ""}
        ${r.decode_tps != null || r.prefill_tps != null ? `<tr><td>吞吐</td><td>decode ${fmtTPS(r.decode_tps)} · prefill ${fmtTPS(r.prefill_tps)}（近似）</td></tr>` : ""}
        ${r.decode_tps != null ? `<tr><td>TPOT</td><td>${fmtTPOT(r.decode_tps)}</td></tr>` : ""}
        <tr><td>HTTP</td><td>${r.http_status ?? "—"}</td></tr>
        ${r.error ? `<tr><td>错误</td><td class="wrap">${esc(r.error)}</td></tr>` : ""}
      </table>
      ${r.output_preview ? `<pre>${esc(r.output_preview)}</pre>` : ""}`;
    box.classList.remove("hidden");
  } catch (e) {
    box.innerHTML = `<p class="form-error">${e.status === 409 ? "正在探测：该对象已有在途请求" : esc(e.message)}</p>`;
    box.classList.remove("hidden");
  } finally {
    btn.disabled = false; btn.textContent = "立即测试";
  }
}

/* ---------- wiring ---------- */

function resetPaging() { pageCursor = null; nextCursor = null; prevCursors = []; }

function closeDetail() {
  currentId = null;
  $("detail").classList.add("hidden");
  $("overview").classList.remove("hidden");
  loadOverview();
}

function init() {
  $("btn-add").addEventListener("click", () => openProviderDialog(null));
  $("btn-settings").addEventListener("click", openSettingsDialog);
  $("settings-cancel").addEventListener("click", () => $("dlg-settings").close());
  $("form-settings").addEventListener("submit", saveSettings);
  $("btn-back").addEventListener("click", closeDetail);
  $("form-provider").addEventListener("submit", saveProvider);
  $("dlg-cancel").addEventListener("click", () => $("dlg-provider").close());
  $("btn-probe").addEventListener("click", manualProbe);
  $("btn-export").addEventListener("click", () => {
    if (currentId == null) return;
    const qs = `provider=${currentId}&revision=${encodeURIComponent(revisionParam())}` +
               `&window=${curWindow}&source=${curSource}`;
    // GET + Content-Disposition triggers a download; no fetch needed.
    const a = document.createElement("a");
    a.href = `/api/report?${qs}`;
    document.body.appendChild(a);
    a.click();
    a.remove();
  });
  $("sel-window").addEventListener("change", (e) => { curWindow = e.target.value; resetPaging(); refreshDetail(); });
  $("sel-revision").addEventListener("change", (e) => { curRevision = e.target.value; resetPaging(); refreshDetail(); });
  $("sel-source").addEventListener("change", (e) => { curSource = e.target.value; resetPaging(); refreshDetail(); });
  $("btn-next").addEventListener("click", () => {
    if (!nextCursor) return;
    prevCursors.push(pageCursor);
    pageCursor = nextCursor;
    refreshDetail();
  });
  $("btn-prev").addEventListener("click", () => {
    if (!prevCursors.length) return;
    pageCursor = prevCursors.pop();
    refreshDetail();
  });
  loadOverview();
  overviewTimer = setInterval(() => { if (currentId == null) loadOverview(); }, 5000);
  setInterval(() => { if (currentId != null) refreshDetail(); }, 5000);
}

document.addEventListener("DOMContentLoaded", init);
