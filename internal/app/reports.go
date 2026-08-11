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

// GenerateReports assembles the STR, CTR, and PEP report rows.
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
	a.logger.InfoContext(ctx, "reports generated", "str", len(str), "ctr", len(ctr), "pep", len(pep), "window", since.Format(time.RFC3339))
	return ReportBundle{GeneratedAt: now, STR: str, CTR: ctr, PEP: pep}, nil
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
	a.renderAdmin(w, "admin_reports.html", r, "Regulator reports", map[string]any{
		"Bundle":    bundle,
		"STRCSV":    strBuf.String(),
		"CTRCSV":    ctrBuf.String(),
		"PEPCSV":    pepBuf.String(),
		"Since":     windowStart(24 * 30 * time.Hour),
		"Threshold": a.cfg.ReportCTRThresholdKobo,
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
