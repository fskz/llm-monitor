package view

import (
	"strconv"
	"strings"

	"llm-monitor/internal/store"
)

// Cache-check presentation (task 10-22-cache-check, design §6). This file
// owns the verdict label table and the CacheCheckView JSON contract consumed
// by web/app.js and the TUI detail block. Machine values travel unchanged
// from internal/cachecheck through the store record; only the human layer
// lives here — the same single-owner rule as StatusText.

// CacheCheckVerdictNone is the empty-state label for a provider that has no
// cache-check record yet: verdict stays "" and verdict_label carries this
// constant (the "未检测" placeholder never masquerades as a verdict).
const CacheCheckVerdictNone = "未检测"

// CacheCheckVerdictText maps a cache-check verdict machine value
// (supported/unsupported/unknown/failed) to its Chinese label. It is the
// single Go source of the labels, the pattern of StatusText; an unrecognized
// value passes through unchanged so a stray machine value stays debuggable.
func CacheCheckVerdictText(verdict string) string {
	switch verdict {
	case "supported":
		return "已开启"
	case "unsupported":
		return "未开启"
	case "unknown":
		return "无法判定"
	case "failed":
		return "检测失败"
	}
	return verdict
}

// CacheCheckHopView is one hop of the two-hop detection evidence. The raw
// numeric pointers pass through from the store record: nil (JSON null) means
// "not observed" and renders as "—", never 0. total_ms is the only always-
// observed duration (probe.Do records elapsed time on every path, including
// failures), so it stays a plain number.
type CacheCheckHopView struct {
	Status       string `json:"status"`
	StatusLabel  string `json:"status_label"`
	TTFTMs       *int64 `json:"ttft_ms"`
	TotalMs      int64  `json:"total_ms"`
	CachedTokens *int64 `json:"cached_tokens"`
	PromptTokens *int   `json:"prompt_tokens"`
}

// CacheCheckView is the response of both POST /api/providers/{id}/cache-check
// (after a fresh run) and GET /api/providers/{id}/cache-check (latest
// persisted record, or the empty state when never checked). Field names and
// null semantics are the contract with web/app.js.
type CacheCheckView struct {
	ProviderID int `json:"provider_id"`
	// Verdict is the machine value; "" means "never checked".
	Verdict string `json:"verdict"`
	// VerdictLabel is the Chinese label, CacheCheckVerdictNone when unchecked.
	VerdictLabel string `json:"verdict_label"`
	// Stale marks a record whose revision no longer matches the provider
	// (base_url/model changed after the check, design §3).
	Stale     bool   `json:"stale"`
	StaleHint string `json:"stale_hint"`
	// CheckedAt is UTC Unix milliseconds, 0 = never checked.
	CheckedAt int64 `json:"checked_at"`
	// Evidence is the machine-readable (English) summary for debugging.
	Evidence string `json:"evidence"`
	// EvidenceText is the human-readable Chinese summary assembled from the
	// record's hops by cacheCheckEvidenceText.
	EvidenceText string              `json:"evidence_text"`
	Hops         []CacheCheckHopView `json:"hops"`
}

// CacheCheckViewOf assembles the view of one provider's cache-check state.
// found reports whether rec is a real record (false renders the "未检测"
// empty state). hops is never nil so the empty state serializes as [], not
// null (JSON empty-collection discipline).
func CacheCheckViewOf(p store.Provider, rec store.CacheCheckRecord, found bool) CacheCheckView {
	v := CacheCheckView{
		ProviderID:   p.ID,
		Verdict:      "",
		VerdictLabel: CacheCheckVerdictNone,
		Hops:         []CacheCheckHopView{},
	}
	if !found {
		return v
	}
	v.Verdict = rec.Verdict
	v.VerdictLabel = CacheCheckVerdictText(rec.Verdict)
	v.CheckedAt = rec.CheckedAt
	v.Evidence = rec.Evidence
	v.EvidenceText = cacheCheckEvidenceText(rec)
	v.Stale = rec.Revision != p.Revision
	if v.Stale {
		v.StaleHint = "配置已变更，建议重新检测"
	}
	v.Hops = make([]CacheCheckHopView, 0, len(rec.Hops))
	for _, h := range rec.Hops {
		v.Hops = append(v.Hops, CacheCheckHopView{
			Status:       h.Status,
			StatusLabel:  StatusText(h.Status),
			TTFTMs:       h.TTFTMs,
			TotalMs:      h.TotalMs,
			CachedTokens: h.CachedTokens,
			PromptTokens: h.PromptTokens,
		})
	}
	return v
}

// cacheCheckEvidenceText assembles the Chinese summary from a stored record:
// both hops' cached_tokens and TTFT numbers, and for a failed run the failed
// hop with its reason. The unknown verdict appends the reference-only TTFT
// wording (prd R1: TTFT is auxiliary evidence and never part of the verdict;
// prd R5: a concurrent probe may pollute the numbers).
func cacheCheckEvidenceText(rec store.CacheCheckRecord) string {
	if rec.Verdict == "failed" {
		if rec.FailedHop >= 1 && rec.FailedHop <= len(rec.Hops) {
			h := rec.Hops[rec.FailedHop-1]
			detail := h.ErrDetail
			if detail == "" {
				detail = "无详细信息"
			}
			return "第 " + strconv.Itoa(rec.FailedHop) + " 跳请求失败：" +
				StatusText(h.Status) + "（" + detail + "）"
		}
		return "检测请求失败"
	}

	var head string
	switch rec.Verdict {
	case "supported":
		head = "前缀缓存已开启"
	case "unsupported":
		head = "未检测到前缀缓存"
	case "unknown":
		head = "上游未返回 cached_tokens，无法判定"
	default:
		head = CacheCheckVerdictText(rec.Verdict)
	}

	var b strings.Builder
	b.WriteString(head)
	b.WriteString("：第一跳 cached_tokens ")
	b.WriteString(cacheCheckTokenText(hopCached(rec, 0)))
	b.WriteString(" / 第二跳 ")
	b.WriteString(cacheCheckTokenText(hopCached(rec, 1)))
	b.WriteString("，TTFT 第一跳 ")
	b.WriteString(cacheCheckTTFTText(hopTTFT(rec, 0)))
	b.WriteString(" / 第二跳 ")
	b.WriteString(cacheCheckTTFTText(hopTTFT(rec, 1)))
	if rec.Verdict == "unknown" {
		b.WriteString("（TTFT 仅供参考，不参与判定）")
	}
	return b.String()
}

// hopCached returns hop i's cached_tokens, or nil when the record has no
// such hop (defensive against a hand-truncated record).
func hopCached(rec store.CacheCheckRecord, i int) *int64 {
	if i >= len(rec.Hops) {
		return nil
	}
	return rec.Hops[i].CachedTokens
}

func hopTTFT(rec store.CacheCheckRecord, i int) *int64 {
	if i >= len(rec.Hops) {
		return nil
	}
	return rec.Hops[i].TTFTMs
}

// cacheCheckTokenText renders a captured cached_tokens count; "缺失" when
// the endpoint stripped the field — never a fabricated 0.
func cacheCheckTokenText(v *int64) string {
	if v == nil {
		return "缺失"
	}
	return strconv.FormatInt(*v, 10)
}

// cacheCheckTTFTText renders one hop's TTFT; "—" when it was not measured
// (unobserved values render the dash, never 0).
func cacheCheckTTFTText(v *int64) string {
	if v == nil {
		return "—"
	}
	return strconv.FormatInt(*v, 10) + "ms"
}
