package store

import (
	"context"
	"time"
)

// OutboxDeliveryOutcomeRows returns the terminal outcome of every WhatsApp
// outbox row whose sent_at landed inside the given trailing window. Only rows
// that reached a terminal state (delivered or failed) are included, so a
// worker still retrying a message does not distort the guard's ratio.
func (s *Store) OutboxDeliveryOutcomeRows(ctx context.Context, window time.Duration) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.outcome FROM message_outbox o
		WHERE o.channel = 'whatsapp'
		  AND o.status IN ('delivered','failed')
		  AND o.sent_at > now() - $1::interval
		ORDER BY o.sent_at`, window)
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
