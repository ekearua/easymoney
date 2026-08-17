// S4: admin SIEM console and CSV/JSON export.
package app

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/store"
)

// adminSIEM renders the SIEM event log with filter controls.
func (a *App) adminSIEM(w http.ResponseWriter, r *http.Request) {
	filter := a.siemFilterFromRequest(r)
	events, err := a.store.ListSIEMEvents(r.Context(), filter)
	if err != nil {
		a.logger.WarnContext(r.Context(), "siem query", "error", err)
		http.Error(w, "SIEM unavailable", http.StatusInternalServerError)
		return
	}
	merchantNames := map[string]string{}
	for _, e := range events {
		var payload map[string]any
		if json.Unmarshal(e.Payload, &payload) != nil {
			continue
		}
		mid, _ := payload["merchant_id"].(string)
		if mid == "" {
			continue
		}
		if _, seen := merchantNames[mid]; seen {
			continue
		}
		midUUID, err := uuid.Parse(mid)
		if err != nil {
			continue
		}
		m, err := a.store.MerchantByID(r.Context(), midUUID)
		if err == nil {
			merchantNames[mid] = m.Name
		}
	}
	a.renderAdmin(w, "admin_siem.html", r, "SIEM Event Log", map[string]any{
		"Events":        events,
		"Filter":        filter,
		"MerchantNames": merchantNames,
		"Result":        r.URL.Query().Get("result"),
		"Error":         r.URL.Query().Get("error"),
	})
}

// adminSIEMExport writes matching events as CSV or JSON download.
func (a *App) adminSIEMExport(w http.ResponseWriter, r *http.Request) {
	filter := a.siemFilterFromRequest(r)
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}

	events, err := a.store.ListSIEMEvents(r.Context(), filter)
	if err != nil {
		a.logger.WarnContext(r.Context(), "siem export", "error", err)
		http.Error(w, "export failed", http.StatusInternalServerError)
		return
	}

	filename := fmt.Sprintf("siem-export-%s.%s", time.Now().UTC().Format("20060102-150405"), format)
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		a.writeSIEMCSV(w, events)
	default:
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(events)
	}
	a.audit(r, "admin.siem.exported", "siem", "", map[string]any{
		"format": format, "count": len(events),
	})
}

func (a *App) siemFilterFromRequest(r *http.Request) store.SIEMFilter {
	f := store.SIEMFilter{
		Topic:      strings.TrimSpace(r.URL.Query().Get("topic")),
		Source:     strings.TrimSpace(r.URL.Query().Get("source")),
		MerchantID: strings.TrimSpace(r.URL.Query().Get("merchant_id")),
		Limit:      1000,
	}
	if s := r.URL.Query().Get("limit"); s != "" {
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
			f.Limit = n
		}
	}
	if s := r.URL.Query().Get("from"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			f.From = &t
		}
	}
	if s := r.URL.Query().Get("to"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			f.To = &t
		}
	}
	return f
}

func (a *App) writeSIEMCSV(w http.ResponseWriter, events []store.SIEMEvent) {
	wr := csv.NewWriter(w)
	_ = wr.Write([]string{"id", "timestamp", "source", "topic", "key", "actor", "actor_type", "ip", "payload"})
	for _, e := range events {
		_ = wr.Write([]string{
			fmt.Sprintf("%d", e.ID),
			e.Timestamp.UTC().Format(time.RFC3339),
			e.Source,
			e.Topic,
			e.Key,
			e.Actor,
			e.ActorType,
			e.IP,
			string(e.Payload),
		})
	}
	wr.Flush()
}
