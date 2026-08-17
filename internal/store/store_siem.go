// S4: SIEM export. A unified query across the business-event outbox and the
// append-only audit log gives compliance operators a single view for security
// monitoring, incident investigation, and regulatory export.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SIEMFilter controls which events are returned by ListSIEMEvents.
type SIEMFilter struct {
	Topic      string     // filter by topic/action prefix (e.g. "payment." or "admin.")
	Source     string     // "business_event" or "audit" — empty = both
	MerchantID string     // filter by merchant_id present in payload
	From       *time.Time // inclusive lower bound
	To         *time.Time // inclusive upper bound
	Limit      int        // max rows (default 1000, cap 10000)
}

// SIEMEvent is one row in the unified SIEM export.
type SIEMEvent struct {
	ID        int64           `json:"id"`
	Timestamp time.Time       `json:"timestamp"`
	Source    string          `json:"source"` // "business_event" | "audit"
	Topic     string          `json:"topic"`  // event topic or audit action
	Key       string          `json:"key"`    // event key or resource_type:resource_id
	Actor     string          `json:"actor"`  // admin email or "system"
	ActorType string          `json:"actor_type"`
	IP        string          `json:"ip,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

// ListSIEMEvents returns a time-ordered, unified view of domain events and
// audit log entries. The query uses a UNION ALL across both tables.
func (s *Store) ListSIEMEvents(ctx context.Context, f SIEMFilter) ([]SIEMEvent, error) {
	if f.Limit <= 0 {
		f.Limit = 1000
	}
	if f.Limit > 10000 {
		f.Limit = 10000
	}

	// Build parameterized WHERE fragments for the business_event arm.
	var beConds []string
	var args []any
	idx := 1
	arg := func(v any) string {
		args = append(args, v)
		p := fmt.Sprintf("$%d", idx)
		idx++
		return p
	}
	if f.From != nil {
		beConds = append(beConds, "created_at >= "+arg(*f.From))
	}
	if f.To != nil {
		beConds = append(beConds, "created_at <= "+arg(*f.To))
	}
	if f.MerchantID != "" {
		beConds = append(beConds, "payload->>'merchant_id' = "+arg(f.MerchantID))
	}
	beWhere := ""
	if len(beConds) > 0 {
		beWhere = " AND " + strings.Join(beConds, " AND ")
	}

	// Build parameterized WHERE fragments for the audit arm.
	var alConds []string
	if f.From != nil {
		alConds = append(alConds, "occurred_at >= "+arg(*f.From))
	}
	if f.To != nil {
		alConds = append(alConds, "occurred_at <= "+arg(*f.To))
	}
	alWhere := ""
	if len(alConds) > 0 {
		alWhere = " AND " + strings.Join(alConds, " AND ")
	}

	// Outer WHERE: source filter and topic filter.
	outerConds := []string{"true"}
	if f.Source == "business_event" {
		outerConds = append(outerConds, "src = 'business_event'")
	} else if f.Source == "audit" {
		outerConds = append(outerConds, "src = 'audit'")
	}
	if f.Topic != "" {
		outerConds = append(outerConds, "topic LIKE "+arg(f.Topic+"%"))
	}

	query := fmt.Sprintf(`
		SELECT id, ts, src, topic, key, actor, actor_type, ip, payload
		FROM (
			SELECT id,
				created_at AS ts,
				'business_event' AS src,
				topic,
				event_key AS key,
				'' AS actor,
				'system' AS actor_type,
				'' AS ip,
				payload
			FROM business_event_outbox
			WHERE true %s
			UNION ALL
			SELECT id,
				occurred_at AS ts,
				'audit' AS src,
				action AS topic,
				COALESCE(resource_type, '') || ':' || COALESCE(resource_id, '') AS key,
				COALESCE(actor_email, actor_type) AS actor,
				actor_type,
				COALESCE(ip, '') AS ip,
				COALESCE(details, '{}'::jsonb) AS payload
			FROM audit_logs
			WHERE true %s
		) events
		WHERE %s
		ORDER BY ts DESC, id DESC
		LIMIT %d`, beWhere, alWhere, strings.Join(outerConds, " AND "), f.Limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("siem query: %w", err)
	}
	defer rows.Close()

	var events []SIEMEvent
	for rows.Next() {
		var e SIEMEvent
		var payload []byte
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Source, &e.Topic, &e.Key, &e.Actor, &e.ActorType, &e.IP, &payload); err != nil {
			return nil, err
		}
		e.Payload = payload
		events = append(events, e)
	}
	return events, rows.Err()
}
