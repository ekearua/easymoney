package app

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"whatsapp-payment-demo/internal/store"
)

// adminMediaReport renders the channel media extraction report: OCR/STT
// success and failure counts per channel per media kind over the last 7 days,
// per-day buckets, and the AI provider's reported token usage so extraction
// spend is visible alongside the messaging cost meter. A per-day token trend
// sparkline makes cost spikes obvious at a glance.
func (a *App) adminMediaReport(w http.ResponseWriter, r *http.Request) {
	report, err := a.store.ChannelMediaStats(r.Context(), 7)
	if err != nil {
		a.logger.WarnContext(r.Context(), "channel media stats failed", "error", err)
		http.Error(w, "could not load channel media report", http.StatusInternalServerError)
		return
	}
	tokenDays, err := a.store.DailyTokens(r.Context(), 7)
	if err != nil {
		a.logger.WarnContext(r.Context(), "daily token stats failed", "error", err)
		http.Error(w, "could not load channel media report", http.StatusInternalServerError)
		return
	}
	a.renderAdmin(w, "admin_media_report.html", r, "Channel media", map[string]any{
		"Report":     report,
		"TokenTrend": mediaTokenTrendSVG(tokenDays),
	})
}

// mediaTokenTrendSVG builds a small server-side SVG area sparkline of AI
// tokens per day: no JS dependency, works under the strict CSP, and scales
// bars against the window's peak so a cost spike is visible at a glance.
// Days render newest-to-oldest right-to-left like the per-day table above it.
func mediaTokenTrendSVG(days []store.TokenDayStat) template.HTML {
	const (
		w      = 560
		h      = 90
		pad    = 4
		barGap = 3
	)
	if len(days) == 0 {
		return template.HTML("")
	}
	peak := int64(0)
	for _, d := range days {
		if d.Tokens > peak {
			peak = d.Tokens
		}
	}
	n := len(days)
	slot := float64(w) / float64(n)
	barW := slot - barGap
	if barW < 4 {
		barW = 4
	}
	var b strings.Builder
	b.WriteString(`<svg class="token-trend" viewBox="0 0 ` + fmt.Sprint(w) + ` ` + fmt.Sprint(h) +
		`" role="img" aria-label="AI tokens per day over the last ` + fmt.Sprint(n) + ` days">`)
	// Peak reference line so relative scale is readable.
	if peak > 0 {
		y := fmt.Sprintf("%.1f", float64(pad))
		b.WriteString(`<line class="tt-peak" x1="0" x2="` + fmt.Sprint(w) + `" y1="` + y + `" y2="` + y + `"/>`)
	}
	for i, d := range days {
		bx := fmt.Sprintf("%.1f", float64(i)*slot+barGap/2)
		var bh float64
		if peak > 0 {
			bh = float64(d.Tokens) / float64(peak) * float64(h-2*pad)
		}
		if bh < 1 && d.Tokens > 0 {
			bh = 1 // keep nonzero days visible even against a huge peak
		}
		by := fmt.Sprintf("%.1f", float64(h)-bh)
		cls := "tt-bar"
		if peak > 0 && d.Tokens > 0 && float64(d.Tokens) >= float64(peak)*0.8 {
			cls += " tt-spike" // near-peak days highlighted as spikes
		}
		label := fmt.Sprintf("%s: %d tokens", d.Day.Format("02 Jan"), d.Tokens)
		b.WriteString(`<rect class="` + cls + `" x="` + bx + `" y="` + by + `" width="` +
			fmt.Sprintf("%.1f", barW) + `" height="` + fmt.Sprintf("%.1f", bh) +
			`"><title>` + label + `</title></rect>`)
	}
	// Day labels under the first and last bars anchor the timeline.
	first := days[0].Day.Format("02 Jan")
	last := days[n-1].Day.Format("02 Jan")
	b.WriteString(`<text class="tt-label" x="0" y="` + fmt.Sprint(h-1) + `">` + first + `</text>`)
	b.WriteString(`<text class="tt-label tt-end" x="` + fmt.Sprint(w) + `" y="` + fmt.Sprint(h-1) + `">` + last + `</text>`)
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}
