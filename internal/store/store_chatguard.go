// C18: chat content guard attempt log. Every inbound chat message that tries to
// send card/PIN/CVV/OTP material is blocked by the conversation service and
// recorded here with the credential redacted, giving compliance a reviewable
// trail (PCI + CBN consumer protection) without persisting payment credentials.
package store

import (
	"context"
	"time"
)

// ChatGuardEvent is one blocked chat-content violation.
type ChatGuardEvent struct {
	ID           int64
	MessageID    string
	Channel      string
	Sender       string
	Recipient    string
	Category     string // card, pin, cvv, otp
	RedactedText string
	CreatedAt    time.Time
}

// RecordChatGuardEvent persists one blocked attempt with the credential already
// redacted by the caller.
func (s *Store) RecordChatGuardEvent(ctx context.Context, event ChatGuardEvent) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO chat_guard_events(message_id,channel,sender,recipient,category,redacted_text)
		VALUES($1,$2,$3,$4,$5,$6)`,
		event.MessageID, event.Channel, event.Sender, event.Recipient, event.Category, event.RedactedText)
	return err
}

// ListChatGuardEvents returns recent blocked attempts, newest first.
func (s *Store) ListChatGuardEvents(ctx context.Context, limit int) ([]ChatGuardEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, message_id, channel, sender, recipient, category, redacted_text, created_at
		FROM chat_guard_events ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []ChatGuardEvent
	for rows.Next() {
		var e ChatGuardEvent
		if err := rows.Scan(&e.ID, &e.MessageID, &e.Channel, &e.Sender,
			&e.Recipient, &e.Category, &e.RedactedText, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ChatGuardAttemptCount returns the total blocked attempts (all time).
func (s *Store) ChatGuardAttemptCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM chat_guard_events`).Scan(&n)
	return n, err
}