// SVG chart rendering for the HTML report. The geometry math mirrors the
// panel's hand-drawn charts (web/app.js bucketPath/xLabels): equal-width
// buckets, max-normalized y, gaps (nil buckets) break the polyline. All
// coordinates are produced by strconv from numeric inputs only — the only
// template.HTML injection point in the report, with no attacker-controlled
// text passing through it.
package view

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"

	"llm-monitor/internal/store"
)

const chartW, chartH = 640, 160

func svgSuccessChart(buckets []store.SeriesBucket) template.HTML {
	has := false
	for _, b := range buckets {
		if b.Samples > 0 {
			has = true
		}
	}
	if !has {
		return `<div class="nodata">暂无样本</div>`
	}
	const PL, PR, PT, PB = 34, 10, 8, 18
	iw, ih := chartW-PL-PR, chartH-PT-PB
	n := len(buckets)
	var b strings.Builder
	// grid 0/50/100%
	for _, v := range []int{0, 50, 100} {
		y := PT + ih - ih*v/100
		b.WriteString(fmt.Sprintf(`<line class="grid" x1="%d" y1="%d" x2="%d" y2="%d"/><text x="%d" y="%d" text-anchor="end">%d%%</text>`,
			PL, y, chartW-PR, y, PL-4, y+3, v))
	}
	b.WriteString(xAxis(buckets, PL, chartW-PR))
	pen := false
	for i, bk := range buckets {
		if bk.OKPct == nil {
			pen = false
			continue
		}
		x := PL + (float64(i)+0.5)*(float64(iw)/float64(n))
		y := PT + float64(ih) - (*bk.OKPct/100)*float64(ih)
		cmd := "L"
		if !pen {
			cmd, pen = "M", true
		}
		b.WriteString(fmt.Sprintf(`<path class="ln ok" d="%s%.1f %.1f"/>`, cmd, x, y))
	}
	return template.HTML(`<svg viewBox="0 0 640 160" preserveAspectRatio="none">` + b.String() + `</svg>`)
}

func svgLatencyChart(buckets []store.SeriesBucket) template.HTML {
	// Mirrors the panel: with no ok samples there is no latency series at all,
	// so show the placeholder instead of an empty grid (web/app.js checks the
	// avg_total_ms-bearing buckets before drawing).
	has := false
	maxV := float64(1000)
	for _, bk := range buckets {
		if bk.AvgTotalMs != nil {
			has = true
			if float64(*bk.AvgTotalMs) > maxV {
				maxV = float64(*bk.AvgTotalMs)
			}
		}
	}
	if !has {
		return `<div class="nodata">暂无成功样本</div>`
	}
	const PL, PR, PT, PB = 44, 10, 8, 18
	iw, ih := chartW-PL-PR, chartH-PT-PB
	n := len(buckets)
	var b strings.Builder
	for k := 0; k <= 2; k++ {
		v := maxV * float64(k) / 2
		y := PT + float64(ih) - float64(ih)*float64(k)/2
		b.WriteString(fmt.Sprintf(`<line class="grid" x1="%d" y1="%.1f" x2="%d" y2="%.1f"/><text x="%d" y="%.1f" text-anchor="end">%s</text>`,
			PL, y, chartW-PR, y, PL-4, y+3, msCompact(int64(v))))
	}
	b.WriteString(xAxis(buckets, PL, chartW-PR))
	b.WriteString(polylineMs(buckets, func(bk store.SeriesBucket) *float64 {
		if bk.AvgTotalMs == nil {
			return nil
		}
		v := float64(*bk.AvgTotalMs)
		return &v
	}, maxV, PL, PT, iw, ih, n, "total"))
	b.WriteString(polylineMs(buckets, func(bk store.SeriesBucket) *float64 {
		if bk.AvgTTFTMs == nil {
			return nil
		}
		v := float64(*bk.AvgTTFTMs)
		return &v
	}, maxV, PL, PT, iw, ih, n, "ttft"))
	b.WriteString(`<text class="leg" x="630" y="154" text-anchor="end"><tspan class="c-total">— 总耗时</tspan><tspan dx="10" class="c-ttft">— TTFT</tspan></text>`)
	return template.HTML(`<svg viewBox="0 0 640 160" preserveAspectRatio="none">` + b.String() + `</svg>`)
}

func polylineMs(buckets []store.SeriesBucket, get func(store.SeriesBucket) *float64, maxV float64, PL, PT, iw, ih int, n int, cls string) string {
	var b strings.Builder
	pen := false
	for i, bk := range buckets {
		v := get(bk)
		if v == nil {
			pen = false
			continue
		}
		x := float64(PL) + (float64(i)+0.5)*(float64(iw)/float64(n))
		y := float64(PT) + float64(ih) - (*v/maxV)*float64(ih)
		cmd := "L"
		if !pen {
			cmd, pen = "M", true
		}
		b.WriteString(fmt.Sprintf(`<path class="ln %s" d="%s%.1f %.1f"/>`, cls, cmd, x, y))
	}
	return b.String()
}

func xAxis(buckets []store.SeriesBucket, left, right int) string {
	if len(buckets) == 0 {
		return ""
	}
	first := timeFmtBucket(buckets[0].StartMs)
	last := timeFmtBucket(buckets[len(buckets)-1].StartMs)
	return fmt.Sprintf(`<text x="%d" y="132">%s</text><text x="%d" y="132" text-anchor="end">%s</text>`,
		left, first, right, last)
}

func timeFmtBucket(ms int64) string {
	return time.UnixMilli(ms).Format("01-02 15:04")
}

func msCompact(v int64) string {
	if v < 1000 {
		return strconv.FormatInt(v, 10) + "ms"
	}
	return strconv.FormatFloat(float64(v)/1000, 'f', 1, 64) + "s"
}
