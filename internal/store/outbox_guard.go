package store

import (
	"context"
	"time"
)

// OutboxDeliveryOutcomeRows returns the terminal status of every WhatsApp
// outbox row whose outcome landed inside the given trailing window. Only rows
// that reached a terminal state (delivered or failed) are included, so a
// worker still retrying a message does not distort the guard's ratio. Rows
// that exhausted their retries without ever reaching the provider never got a
// sent_at stamp, so they are windowed on created_at instead — otherwise the
// guard would be blind to exactly the provider outage it exists for.
func (s *Store) OutboxDeliveryOutcomeRows(ctx context.Context, window time.Duration) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.status FROM message_outbox o
		WHERE o.channel = 'whatsapp'
		  AND o.status IN ('delivered','failed')
		  AND COALESCE(o.sent_at, o.created_at) > now() - $1::interval
		ORDER BY COALESCE(o.sent_at, o.created_at)`, window)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var outcome string
		if err := rows.Scan(&outcome); err != nil {
			return nil, err
		}
		out = append(out, outcome)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
