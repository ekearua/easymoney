// C15: regulator report generation. GenerateReports writes NFIU/CBN-format
// STR, CTR, and PEP files from audited store data. The admin surface renders
// the same rows and exposes CSV downloads to compliance admins.
package app

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

	"whatsapp-payment-demo/internal/reports"
	"whatsapp-payment-demo/internal/store"
)

// ReportBundle holds one of each regulator report generated at the same
// instant, used by both the admin page and the CLI export.
type ReportBundle struct {
	GeneratedAt time.Time
	STR         []reports.STRRow
	CTR         []reports.CTRRow
	PEP         []reports.PEPRow
}

// windowStart returns the reporting window start for the given lookback.
func windowStart(lookback time.Duration) time.Time {
	if lookback <= 0 {
		lookback = 24 * 30 * time.Hour
	}
	return time.Now().UTC().Add(-lookback)
}

// GenerateReports assembles the STR, CTR, and PEP report rows. Every
// generation re-affirms the filing clocks (spec G5): report rows are
// recomputed from audited data, so the sync keyed on each row's stable
// reference is idempotent, and clocks whose underlying alert has reached a
// terminal status are stamped complete.
func (a *App) GenerateReports(ctx context.Context, lookback time.Duration) (ReportBundle, error) {
	now := time.Now().UTC()
	since := windowStart(lookback)
	str, err := a.store.STRReport(ctx, since, 500)
	if err != nil {
		return ReportBundle{}, err
	}
	ctr, err := a.store.CTRReport(ctx, since, a.cfg.ReportCTRThresholdKobo, 500)
	if err != nil {
		return ReportBundle{}, err
	}
	pep, err := a.store.PEPReport(ctx, 500)
	if err != nil {
		return ReportBundle{}, err
	}
	bundle := ReportBundle{GeneratedAt: now, STR: str, CTR: ctr, PEP: pep}
	if err := a.syncFilingClocks(ctx, bundle); err != nil {
		// Clock tracking must never break report generation; the admin page
		// surfaces the sync problem via the clocks panel instead.
		a.logger.WarnContext(ctx, "filing clock sync failed", "error", err)
	}
	a.logger.InfoContext(ctx, "reports generated", "str", len(str), "ctr", len(ctr), "pep", len(pep), "window", since.Format(time.RFC3339))
	return bundle, nil
}

// syncFilingClocks mirrors one report bundle into report_filing_clocks: an
// STR clock per alert-backed transaction (24h deadline), a CTR clock per
// large payment (7th of the following month), a PEP clock per determined
// customer (monthly registry cadence). STR clocks whose alert status is
// terminal (resolved / false_positive) stamp complete — the case behind the
// report closed.
func (a *App) syncFilingClocks(ctx context.Context, b ReportBundle) error {
	strRefs := make([]string, 0, len(b.STR))
	strDates := make([]time.Time, 0, len(b.STR))
	strDone := make([]string, 0)
	seen := map[string]bool{}
	for _, r := range b.STR {
		ref := store.FilingClockRef("STR", r.TransactionID.String())
		if !seen[ref] {
			seen[ref] = true
			strRefs = append(strRefs, ref)
			strDates = append(strDates, r.ReportDate)
		}
		if r.AlertStatus == "resolved" || r.AlertStatus == "false_positive" {
			strDone = append(strDone, ref)
		}
	}
	if len(strRefs) > 0 {
		if err := a.store.SyncReportFilingClocks(ctx, "STR", strRefs, strDates, b.GeneratedAt); err != nil {
			return err
		}
	}
	if len(strDone) > 0 {
		if _, err := a.store.CompleteReportFilingClocks(ctx, "STR", strDone, b.GeneratedAt); err != nil {
			return err
		}
	}
	ctrRefs := make([]string, 0, len(b.CTR))
	ctrDates := make([]time.Time, 0, len(b.CTR))
	seen = map[string]bool{}
	for _, r := range b.CTR {
		ref := store.FilingClockRef("CTR", r.TransactionID.String())
		if !seen[ref] {
			seen[ref] = true
			ctrRefs = append(ctrRefs, ref)
			ctrDates = append(ctrDates, r.TransactionDate)
		}
	}
	if len(ctrRefs) > 0 {
		if err := a.store.SyncReportFilingClocks(ctx, "CTR", ctrRefs, ctrDates, b.GeneratedAt); err != nil {
			return err
		}
	}
	pepRefs := make([]string, 0, len(b.PEP))
	pepDates := make([]time.Time, 0, len(b.PEP))
	seen = map[string]bool{}
	for _, r := range b.PEP {
		ref := store.FilingClockRef("PEP", r.CustomerID.String())
		if !seen[ref] {
			seen[ref] = true
			pepRefs = append(pepRefs, ref)
			pepDates = append(pepDates, r.ScreenedAt)
		}
	}
	if len(pepRefs) > 0 {
		if err := a.store.SyncReportFilingClocks(ctx, "PEP", pepRefs, pepDates, b.GeneratedAt); err != nil {
			return err
		}
	}
	return nil
}

// filingClockView is one rendered row of the filing-clock panel: the clock
// plus its display state so the template stays logic-free.
type filingClockView struct {
	Clock       store.ReportFilingClock
	Status      string
	StatusClass string
	OverdueDays int
}

// filingClockViews loads and renders the clock surface for the admin page.
func (a *App) filingClockViews(ctx context.Context, now time.Time) ([]filingClockView, int) {
	clocks, err := a.store.ReportFilingClocks(ctx, "", 200)
	if err != nil {
		a.logger.WarnContext(ctx, "load filing clocks", "error", err)
		return nil, 0
	}
	views := make([]filingClockView, 0, len(clocks))
	overdue := 0
	for _, c := range clocks {
		v := filingClockView{Clock: c}
		switch {
		case c.FiledAt != nil:
			v.Status = "filed " + c.FiledAt.Format("02 Jan 2006")
			v.StatusClass = "ok"
		case now.After(c.DueAt):
			v.OverdueDays = int(now.Sub(c.DueAt).Hours() / 24)
			v.Status = fmt.Sprintf("OVERDUE by %d day(s)", v.OverdueDays)
			v.StatusClass = "warn"
			overdue++
		default:
			days := int(time.Until(c.DueAt).Hours() / 24)
			if days < 1 {
				v.Status = "due within 24h"
				v.StatusClass = "warn"
			} else {
				v.Status = fmt.Sprintf("due in %d day(s)", days)
				v.StatusClass = ""
			}
		}
		views = append(views, v)
	}
	return views, overdue
}

// ExportReportsCSV writes the three reports as CSV files into dir (created if
// needed) and returns the written filenames.
func (a *App) ExportReportsCSV(ctx context.Context, dir string, lookback time.Duration) ([]string, error) {
	bundle, err := a.GenerateReports(ctx, lookback)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create report dir: %w", err)
	}
	stamp := bundle.GeneratedAt.Format("20060102-150405")
	files := []struct {
		name string
		body func(*bytes.Buffer) error
	}{
		{"STR-" + stamp + ".csv", func(b *bytes.Buffer) error { return reports.STRCSV(b, bundle.STR, bundle.GeneratedAt) }},
		{"CTR-" + stamp + ".csv", func(b *bytes.Buffer) error { return reports.CTRCSV(b, bundle.CTR, bundle.GeneratedAt) }},
		{"PEP-" + stamp + ".csv", func(b *bytes.Buffer) error { return reports.PEPCSV(b, bundle.PEP, bundle.GeneratedAt) }},
	}
	var written []string
	for _, f := range files {
		var buf bytes.Buffer
		if err := f.body(&buf); err != nil {
			return written, fmt.Errorf("encode %s: %w", f.name, err)
		}
		path := filepath.Join(dir, f.name)
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			return written, fmt.Errorf("write %s: %w", path, err)
		}
		written = append(written, path)
		a.logger.InfoContext(ctx, "report file written", "path", path, "bytes", buf.Len())
	}
	return written, nil
}

// adminReports renders the three regulator reports for compliance staff with
// download links for each format.
func (a *App) adminReports(w http.ResponseWriter, r *http.Request) {
	bundle, err := a.GenerateReports(r.Context(), 24*30*time.Hour)
	if err != nil {
		a.logger.WarnContext(r.Context(), "generate reports", "error", err)
		http.Error(w, "reports unavailable", http.StatusInternalServerError)
		return
	}
	var strBuf, ctrBuf, pepBuf bytes.Buffer
	_ = reports.STRCSV(&strBuf, bundle.STR, bundle.GeneratedAt)
	_ = reports.CTRCSV(&ctrBuf, bundle.CTR, bundle.GeneratedAt)
	_ = reports.PEPCSV(&pepBuf, bundle.PEP, bundle.GeneratedAt)
	clocks, overdue := a.filingClockViews(r.Context(), time.Now().UTC())
	a.renderAdmin(w, "admin_reports.html", r, "Regulator reports", map[string]any{
		"Bundle":       bundle,
		"STRCSV":       strBuf.String(),
		"CTRCSV":       ctrBuf.String(),
		"PEPCSV":       pepBuf.String(),
		"Since":        windowStart(24 * 30 * time.Hour),
		"Threshold":    a.cfg.ReportCTRThresholdKobo,
		"FilingClocks": clocks,
		"OverdueCount": overdue,
	})
}

// adminReportDownload streams a single regulator report as a CSV attachment.
func (a *App) adminReportDownload(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	lookback := 24 * 30 * time.Hour
	if since := r.URL.Query().Get("since"); since != "" {
		if parsed, err := time.Parse(time.RFC3339, since); err == nil {
			lookback = time.Since(parsed)
		}
	}
	bundle, err := a.GenerateReports(r.Context(), lookback)
	if err != nil {
		http.Error(w, "reports unavailable", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	stamp := bundle.GeneratedAt.Format("20060102-150405")
	switch kind {
	case "str":
		if err := reports.STRCSV(&buf, bundle.STR, bundle.GeneratedAt); err != nil {
			http.Error(w, "encode failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="STR-%s.csv"`, stamp))
	case "ctr":
		if err := reports.CTRCSV(&buf, bundle.CTR, bundle.GeneratedAt); err != nil {
			http.Error(w, "encode failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="CTR-%s.csv"`, stamp))
	case "pep":
		if err := reports.PEPCSV(&buf, bundle.PEP, bundle.GeneratedAt); err != nil {
			http.Error(w, "encode failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="PEP-%s.csv"`, stamp))
	case "str-goaml":
		body, err := reports.STRgoAML(bundle.STR, bundle.GeneratedAt)
		if err == nil {
			err = reports.ValidateGoAML(body)
		}
		if err != nil {
			http.Error(w, "goAML encoding failed validation", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="STR-%s.goaml.xml"`, stamp))
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(body)
		return
	case "ctr-goaml":
		body, err := reports.CTRgoAML(bundle.CTR, bundle.GeneratedAt)
		if err == nil {
			err = reports.ValidateGoAML(body)
		}
		if err != nil {
			http.Error(w, "goAML encoding failed validation", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="CTR-%s.goaml.xml"`, stamp))
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(body)
		return
	case "pep-goaml":
		body, err := reports.PEPgoAML(bundle.PEP, bundle.GeneratedAt)
		if err == nil {
			err = reports.ValidateGoAML(body)
		}
		if err != nil {
			http.Error(w, "goAML encoding failed validation", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="PEP-%s.goaml.xml"`, stamp))
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(body)
		return
	default:
		http.Error(w, "unknown report type", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(buf.Bytes()); err != nil {
		a.logger.WarnContext(r.Context(), "write report download", "kind", kind, "error", err)
	}
}
