package app

import (
	"html/template"
	"strings"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/store"
	"whatsapp-payment-demo/web"
)

// TestBuildBackupPanel pins the alert semantics of the backup/PITR panel:
// an on-schedule heartbeat reads as fresh, a missed window reads as stale,
// a heartbeat that never ran is always stale (unless the alert is off), a
// zero threshold disables the row's alert without hiding it, and the
// archiver cross-check only reports failing when the newest attempt is a
// failure — an old failure superseded by a later success stays quiet.
func TestBuildBackupPanel(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	future := now.Add(2 * time.Hour) // clock skew must clamp, never go negative

	cases := []struct {
		name    string
		health  store.BackupHealth
		base    time.Duration
		wal     time.Duration
		drill   time.Duration
		stale   []string // labels expected stale
		fresh   []string
		archOK  bool
		archBad bool
		archUnk bool
	}{
		{
			name: "all on schedule",
			health: store.BackupHealth{
				LastBaseBackupAt:   ago(3 * time.Hour),
				LastWALShipAt:      ago(10 * time.Minute),
				LastRestoreDrillAt: ago(2 * 24 * time.Hour),
				LastArchivedWALAt:  ago(8 * time.Minute),
			},
			base: 26 * time.Hour, wal: 45 * time.Minute, drill: 8 * 24 * time.Hour,
			fresh:  []string{"Base backup", "WAL ship (offsite)", "PITR restore drill"},
			archOK: true,
		},
		{
			name: "wal ship missed its window",
			health: store.BackupHealth{
				LastBaseBackupAt:  ago(3 * time.Hour),
				LastWALShipAt:     ago(2 * time.Hour), // 45m threshold
				LastArchivedWALAt: ago(8 * time.Minute),
			},
			base: 26 * time.Hour, wal: 45 * time.Minute, drill: 8 * 24 * time.Hour,
			stale:  []string{"WAL ship (offsite)"},
			fresh:  []string{"Base backup"},
			archOK: true,
		},
		{
			name: "never ran is always stale",
			health: store.BackupHealth{
				LastBaseBackupAt: ago(time.Hour),
			},
			base: 26 * time.Hour, wal: 45 * time.Minute, drill: 8 * 24 * time.Hour,
			stale: []string{"WAL ship (offsite)", "PITR restore drill"},
			fresh: []string{"Base backup"},
		},
		{
			name:   "zero threshold disables but still renders",
			health: store.BackupHealth{},
			base:   0, wal: 0, drill: 0,
			fresh:  []string{"Base backup", "WAL ship (offsite)", "PITR restore drill"},
			archOK: true, // no archiver failure recorded either
		},
		{
			name: "never ran with alert off is not stale",
			health: store.BackupHealth{
				LastBaseBackupAt: ago(time.Hour),
			},
			base: 26 * time.Hour, wal: 0, drill: 8 * 24 * time.Hour,
			fresh: []string{"WAL ship (offsite)"},
			stale: []string{"PITR restore drill"},
		},
		{
			name: "archiver failure older than success is quiet",
			health: store.BackupHealth{
				LastBaseBackupAt:   ago(time.Hour),
				LastWALShipAt:      ago(10 * time.Minute),
				LastRestoreDrillAt: ago(2 * 24 * time.Hour),
				LastArchivedWALAt:  ago(5 * time.Minute),
				LastWALFailedAt:    ago(30 * time.Minute), // superseded
				LastFailedWAL:      "0000000A",
			},
			base: 26 * time.Hour, wal: 45 * time.Minute, drill: 8 * 24 * time.Hour,
			archOK: true,
		},
		{
			name: "archiver failure newer than success is failing",
			health: store.BackupHealth{
				LastBaseBackupAt:   ago(time.Hour),
				LastWALShipAt:      ago(10 * time.Minute),
				LastRestoreDrillAt: ago(2 * 24 * time.Hour),
				LastArchivedWALAt:  ago(40 * time.Minute),
				LastWALFailedAt:    ago(5 * time.Minute), // newest attempt failed
				LastFailedWAL:      "0000000B",
			},
			base: 26 * time.Hour, wal: 45 * time.Minute, drill: 8 * 24 * time.Hour,
			archBad: true,
		},
		{
			name: "archiver stats unreadable is unknown, not failing",
			health: store.BackupHealth{
				LastBaseBackupAt:    ago(time.Hour),
				LastWALShipAt:       ago(10 * time.Minute),
				LastRestoreDrillAt:  ago(2 * 24 * time.Hour),
				ArchiverUnavailable: "permission denied for pg_stat_archiver",
			},
			base: 26 * time.Hour, wal: 45 * time.Minute, drill: 8 * 24 * time.Hour,
			archUnk: true,
		},
		{
			name: "future-stamped heartbeat clamps instead of negative age",
			health: store.BackupHealth{
				LastBaseBackupAt:   &future,
				LastWALShipAt:      ago(10 * time.Minute),
				LastRestoreDrillAt: ago(2 * 24 * time.Hour),
			},
			base: 26 * time.Hour, wal: 45 * time.Minute, drill: 8 * 24 * time.Hour,
			fresh: []string{"Base backup"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			panel := buildBackupPanel(tc.health, now, tc.base, tc.wal, tc.drill)
			if len(panel.Rows) != 3 {
				t.Fatalf("expected 3 rows, got %d", len(panel.Rows))
			}
			byLabel := map[string]BackupStatRow{}
			for _, r := range panel.Rows {
				byLabel[r.Label] = r
			}
			for _, label := range tc.stale {
				if r, ok := byLabel[label]; !ok || !r.Stale {
					t.Errorf("%s: expected stale, got %+v", label, r)
				}
			}
			for _, label := range tc.fresh {
				if r, ok := byLabel[label]; !ok || r.Stale {
					t.Errorf("%s: expected fresh, got %+v", label, r)
				}
			}
			if panel.ArchiverFailing != tc.archBad {
				t.Errorf("ArchiverFailing = %v, want %v", panel.ArchiverFailing, tc.archBad)
			}
			if panel.ArchiverUnknown != tc.archUnk {
				t.Errorf("ArchiverUnknown = %v, want %v", panel.ArchiverUnknown, tc.archUnk)
			}
			if !tc.archBad && !tc.archUnk && panel.ArchiverError != "" {
				t.Errorf("quiet archiver must carry no error, got %q", panel.ArchiverError)
			}
			// AnyStale must agree with the row verdicts exactly.
			wantStale := len(tc.stale) > 0
			if panel.AnyStale != wantStale {
				t.Errorf("AnyStale = %v, want %v", panel.AnyStale, wantStale)
			}
		})
	}
}

// TestFormatAgeAndThreshold pins the humanised units the panel renders:
// minutes under an hour, hours+minutes under two days, days beyond, and the
// threshold rendered in its natural unit ("45m", "26h", "8d", "off").
func TestFormatAgeAndThreshold(t *testing.T) {
	t.Parallel()
	ageCases := []struct {
		in   time.Duration
		want string
	}{
		{-5 * time.Second, "just now"}, // clamp, never negative
		{30 * time.Second, "just now"},
		{7 * time.Minute, "7m"},
		{90 * time.Minute, "1h30m"},
		{26 * time.Hour, "26h00m"},
		{50 * time.Hour, "2d02h"},
	}
	for _, c := range ageCases {
		if got := FormatAge(c.in); got != c.want {
			t.Errorf("FormatAge(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	thCases := []struct {
		in   time.Duration
		want string
	}{
		{0, "off"},
		{-1 * time.Hour, "off"},
		{45 * time.Minute, "45m"},
		{26 * time.Hour, "26h"},
		{8 * 24 * time.Hour, "8d"},
	}
	for _, c := range thCases {
		if got := FormatThreshold(c.in); got != c.want {
			t.Errorf("FormatThreshold(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestMetricsTemplateBackupPanel renders metrics.html with a stale panel and
// pins that the stale age is visually flagged and the reason is spelled out,
// plus that a panel with everything fresh renders no stale hint. Runs against
// the parsed template set so drift in the template is caught here.
func TestMetricsTemplateBackupPanel(t *testing.T) {
	t.Parallel()
	tmpl := parseTOTPTestTemplates(t)
	buf := new(strings.Builder)

	data := map[string]any{
		"AppName": "Xego", "Title": "Metrics", "CSRF": "x", "AdminRole": "admin",
		"Metrics": store.Metrics{},
		"BackupPanel": buildBackupPanel(store.BackupHealth{
			LastBaseBackupAt: ptrTime(time.Now().Add(-30 * time.Hour)),
		}, time.Now(), 26*time.Hour, 45*time.Minute, 8*24*time.Hour),
	}
	if err := tmpl.ExecuteTemplate(buf, "metrics.html", data); err != nil {
		t.Fatalf("execute metrics.html with backup panel: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Backup &amp; PITR health") {
		t.Error("panel heading missing")
	}
	if !strings.Contains(out, `class="danger-text"`) {
		t.Error("a stale age must render with danger-text")
	}
	if !strings.Contains(out, "over 26h") {
		t.Error("stale row must state the threshold it crossed")
	}
	// The fixture has no WAL/drill heartbeats, so those rows read "never"
	// and cross their thresholds too.
	if !strings.Contains(out, "over 45m") {
		t.Error("a never-ran row must state its crossed threshold")
	}
	if !strings.Contains(out, "never") {
		t.Error("a missing heartbeat must render as never")
	}

	// Fully-fresh panel: every row reads within threshold and no age is
	// flagged, plus the disabled-threshold wording is covered by setting
	// the drill alert off.
	buf.Reset()
	freshData := map[string]any{
		"AppName": "Xego", "Title": "Metrics", "CSRF": "x", "AdminRole": "admin",
		"Metrics": store.Metrics{},
		"BackupPanel": buildBackupPanel(store.BackupHealth{
			LastBaseBackupAt:   ptrTime(time.Now().Add(-3 * time.Hour)),
			LastWALShipAt:      ptrTime(time.Now().Add(-10 * time.Minute)),
			LastRestoreDrillAt: ptrTime(time.Now().Add(-48 * time.Hour)),
			LastArchivedWALAt:  ptrTime(time.Now().Add(-8 * time.Minute)),
		}, time.Now(), 26*time.Hour, 45*time.Minute, 0),
	}
	if err := tmpl.ExecuteTemplate(buf, "metrics.html", freshData); err != nil {
		t.Fatalf("execute metrics.html with fresh panel: %v", err)
	}
	fresh := buf.String()
	if strings.Contains(fresh, `class="danger-text"`) {
		t.Error("a fresh panel must not flag any age")
	}
	if !strings.Contains(fresh, "within 26h") {
		t.Error("fresh row must state it is within threshold")
	}
	if !strings.Contains(fresh, "alert off") {
		t.Error("a zero threshold must render as alert off")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// parseTOTPTestTemplates is the shared test helper for TOTP/security tests: it
// parses the full embedded template set (including the screening-cost, synthetic-
// checks and media-report panels that were added later) with a minimal but
// complete function map, so those tests still parse when the template set grows.
func parseTOTPTestTemplates(t *testing.T) *template.Template {
	t.Helper()
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"money":             func(k int64) string { return "NGN" },
		"maskPII":           func(s string) string { return s },
		"statusClass":       func(any) string { return "" },
		"percent":           func(float64) string { return "" },
		"sub":               func(a, b int64) int64 { return a - b },
		"add":               func(a, b int64) int64 { return a + b },
		"mul":               func(a, b int64) int64 { return a * b },
		"divf":              func(a, b float64, c float64) float64 { if b == 0 { return 0 } ; return a / b * c },
		"inc":               func(i int) int { return i + 1 },
		"join":              func(items []string, sep string) string { return strings.Join(items, sep) },
		"collectionFeeKobo": func(p store.PaymentView) int64 { return 0 },
		"date":              func(any) string { return "" },
	}).ParseFS(web.Assets, "templates/*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	return tmpl
}
