package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"whatsapp-payment-demo/internal/domain"
)

// LegalHold is one retention-exempt subject (C29). While a hold is active the
// purge worker never archives or deletes that subject.
type LegalHold struct {
	ID          int64
	SubjectType string
	SubjectID   string
	Reason      string
	CreatedBy   string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
}

// AddLegalHold freezes a subject so retention never purges it. A NULL
// expiresAt means the hold is indefinite.
func (s *Store) AddLegalHold(ctx context.Context, subjectType, subjectID, reason, createdBy string, expiresAt *time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO legal_holds(subject_type, subject_id, reason, created_by, expires_at)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (subject_type, subject_id)
		DO UPDATE SET reason=EXCLUDED.reason, expires_at=EXCLUDED.expires_at`,
		subjectType, subjectID, reason, createdBy, expiresAt)
	return err
}

// RemoveLegalHold clears a freeze so normal retention applies again.
func (s *Store) RemoveLegalHold(ctx context.Context, subjectType, subjectID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM legal_holds WHERE subject_type=$1 AND subject_id=$2`, subjectType, subjectID)
	return err
}

// ListLegalHolds returns every active hold, newest first.
func (s *Store) ListLegalHolds(ctx context.Context) ([]LegalHold, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, subject_type, subject_id, reason, created_by, created_at, expires_at
		FROM legal_holds
		WHERE expires_at IS NULL OR expires_at > now()
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var holds []LegalHold
	for rows.Next() {
		var h LegalHold
		if err := rows.Scan(&h.ID, &h.SubjectType, &h.SubjectID, &h.Reason, &h.CreatedBy, &h.CreatedAt, &h.ExpiresAt); err != nil {
			return nil, err
		}
		holds = append(holds, h)
	}
	return holds, rows.Err()
}

// ArchiveLedgerEntry is one append-only snapshot of a record removed by retention.
type ArchiveLedgerEntry struct {
	ID         int64
	ArchivedAt time.Time
	RecordType string
	SubjectID  string
	Payload    map[string]any
	PrevHash   string
	Hash       string
}

// ListArchiveLedger returns recent archived snapshots, newest first.
func (s *Store) ListArchiveLedger(ctx context.Context, limit int) ([]ArchiveLedgerEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, archived_at, record_type, subject_id, payload, prev_hash, hash
		FROM archive_ledger ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ArchiveLedgerEntry
	for rows.Next() {
		var e ArchiveLedgerEntry
		var payload []byte
		if err := rows.Scan(&e.ID, &e.ArchivedAt, &e.RecordType, &e.SubjectID, &payload, &e.PrevHash, &e.Hash); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(payload, &e.Payload)
		if e.Payload == nil {
			e.Payload = map[string]any{}
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// purgeBatch deletes up to limit rows from table ordered by keyColumn and
// matching the predicate (a WHERE fragment). Returns rows removed. Designed so
// retention never holds a table-wide lock for long.
func (s *Store) purgeBatch(ctx context.Context, table, keyColumn, predicate string, limit int, args ...any) (int64, error) {
	var total int64
	for {
		q := fmt.Sprintf(`DELETE FROM %s WHERE %s IN (
			SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT %d)`,
			table, keyColumn, keyColumn, table, predicate, keyColumn, limit)
		tag, err := s.pool.Exec(ctx, q, args...)
		if err != nil {
			return total, err
		}
		n := tag.RowsAffected()
		total += n
		if n < int64(limit) {
			break
		}
	}
	return total, nil
}

// archiveHash chains an archive snapshot to its predecessor, mirroring the
// audit log's tamper-evident scheme.
func archiveHash(prevHash, recordType, subjectID string, payload []byte) string {
	sum := sha256.Sum256([]byte(prevHash + "|" + recordType + "|" + subjectID + "|" + string(payload)))
	return hex.EncodeToString(sum[:])
}

// appendArchive writes one immutable snapshot into archive_ledger. The insert
// is serialized on the tail row so the hash chain cannot fork.
func (s *Store) appendArchive(ctx context.Context, tx pgx.Tx, recordType, subjectID string, payload []byte) error {
	var prevHash string
	if err := tx.QueryRow(ctx, `SELECT hash FROM archive_ledger ORDER BY id DESC LIMIT 1 FOR UPDATE`).Scan(&prevHash); err != nil && err != pgx.ErrNoRows {
		return err
	}
	hash := archiveHash(prevHash, recordType, subjectID, payload)
	_, err := tx.Exec(ctx, `
		INSERT INTO archive_ledger(record_type, subject_id, payload, prev_hash, hash)
		VALUES($1,$2,$3,$4,$5)`, recordType, subjectID, payload, prevHash, hash)
	return err
}

// archivedSubject returns true when a legal hold covers a subject, so the purge
// worker skips it.
func (s *Store) archivedSubject(ctx context.Context, tx pgx.Tx, subjectType, subjectID string) (bool, error) {
	var found bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM legal_holds
			WHERE subject_type=$1 AND subject_id=$2 AND (expires_at IS NULL OR expires_at > now())
		)`, subjectType, subjectID).Scan(&found)
	return found, err
}

// PurgeReport summarizes one retention run.
type PurgeReport struct {
	UsersPurged       int64
	PaymentsArchived  int64
	PaymentsPurged    int64
	InvoicesArchived  int64
	InvoicesPurged    int64
	ThriftPurged      int64
	OperationalPurged int64
	AuditArchived     int64
	AuditPurged       int64
	RowsSkippedByHold int64
}

// PurgeBefore removes demo personal data and operational records older than
// the cutoff. Financial records (payments, invoices, thrift) are snapshotted
// into the append-only archive ledger BEFORE they are hard-deleted, so MLPA
// record keeping survives retention. Every delete is batched (keyset, LIMIT)
// and legal holds are respected at the user and record level.
func (s *Store) PurgeBefore(ctx context.Context, cutoff time.Time) (PurgeReport, error) {
	var report PurgeReport
	batch := 500

	holds, err := s.ListLegalHolds(ctx)
	if err != nil {
		return report, err
	}
	report.RowsSkippedByHold = int64(len(holds))

	// Expired sessions, tokens and verification codes.
	if _, err := s.purgeBatch(ctx, "admin_sessions", "token_hash", `expires_at < now()`, batch); err != nil {
		return report, err
	}
	if _, err := s.purgeBatch(ctx, "merchant_sessions", "token_hash", `expires_at < now()`, batch); err != nil {
		return report, err
	}
	if _, err := s.purgeBatch(ctx, "totp_pending_logins", "token_hash", `expires_at < now()`, batch); err != nil {
		return report, err
	}
	if _, err := s.purgeBatch(ctx, "merchant_password_reset_tokens", "token_hash", `expires_at < now()`, batch); err != nil {
		return report, err
	}
	if _, err := s.purgeBatch(ctx, "email_verification_codes", "id", `expires_at < now() OR created_at < $1`, batch, cutoff); err != nil {
		return report, err
	}
	if _, err := s.purgeBatch(ctx, "conversation_sessions", "user_id", `expires_at < now() OR updated_at < $1`, batch, cutoff); err != nil {
		return report, err
	}

	// Operational chat/webhook/sms data.
	for _, t := range []struct {
		table, key, pred string
	}{
		{"webhook_deliveries", "id", `received_at < $1`},
		{"merchant_webhook_deliveries", "id", `created_at < $1`},
		{"inbound_messages", "provider_message_id", `received_at < $1`},
		{"message_outbox", "id", `created_at < $1`},
		{"sms_requests", "provider_message_id", `received_at < $1`},
		{"merchant_registrations", "id", `created_at < $1`},
	} {
		n, err := s.purgeBatch(ctx, t.table, t.key, t.pred, batch, cutoff)
		if err != nil {
			return report, err
		}
		report.OperationalPurged += n
	}

	// Financial records: archive first, then hard-delete in batches.
	paymentsArchived, paymentsPurged, err := s.archiveAndPurgePayments(ctx, cutoff)
	if err != nil {
		return report, err
	}
	report.PaymentsArchived = paymentsArchived
	report.PaymentsPurged = paymentsPurged

	invoicesArchived, invoicesPurged, err := s.archiveAndPurgeInvoices(ctx, cutoff)
	if err != nil {
		return report, err
	}
	report.InvoicesArchived = invoicesArchived
	report.InvoicesPurged = invoicesPurged

	thriftPurged, err := s.archiveAndPurgeThrift(ctx, cutoff)
	if err != nil {
		return report, err
	}
	report.ThriftPurged = thriftPurged

	// Data orders reference payments (SET NULL on delete) and users (cascade).
	n, err := s.purgeBatch(ctx, "data_orders", "id", `created_at < $1`, batch, cutoff)
	if err != nil {
		return report, err
	}
	report.OperationalPurged += n

	auditArchived, auditPurged, err := s.archiveAndPurgeAudit(ctx, cutoff)
	if err != nil {
		return report, err
	}
	report.AuditArchived = auditArchived
	report.AuditPurged = auditPurged

	// Users: only those inactive beyond the cutoff with no financial footprint
	// and no active legal hold. Everything else cascades or is skipped.
	for {
		var id uuid.UUID
		err := s.pool.QueryRow(ctx, `
			SELECT u.id FROM users u
			WHERE u.updated_at < $1
			  AND NOT EXISTS (SELECT 1 FROM payments p WHERE p.user_id=u.id)
			  AND NOT EXISTS (SELECT 1 FROM invoices i WHERE i.created_by_user_id=u.id)
			  AND NOT EXISTS (SELECT 1 FROM thrift_groups tg WHERE tg.creator_user_id=u.id)
			  AND NOT EXISTS (SELECT 1 FROM thrift_members tm WHERE tm.user_id=u.id)
			  AND NOT EXISTS (SELECT 1 FROM merchant_owners mo WHERE mo.user_id=u.id)
			  AND NOT EXISTS (SELECT 1 FROM legal_holds h WHERE h.subject_type='user' AND h.subject_id=u.id::text
			    AND (h.expires_at IS NULL OR h.expires_at > now()))
			ORDER BY u.id LIMIT 1`, cutoff).Scan(&id)
		if err == pgx.ErrNoRows {
			break
		}
		if err != nil {
			return report, err
		}
		tag, err := s.pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, id)
		if err != nil {
			return report, err
		}
		if tag.RowsAffected() == 0 {
			break
		}
		report.UsersPurged++
	}
	return report, nil
}

// archiveAndPurgePayments snapshots payments (and their events) into the ledger
// before deleting them, skipping anything under a legal hold.
func (s *Store) archiveAndPurgePayments(ctx context.Context, cutoff time.Time) (int64, int64, error) {
	var archived, purged int64
	for {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return archived, purged, err
		}
		rows, err := tx.Query(ctx, `
			SELECT p.id FROM payments p
			WHERE p.created_at < $1
			  AND NOT EXISTS (SELECT 1 FROM legal_holds h WHERE h.subject_type='payment' AND h.subject_id=p.id::text
			    AND (h.expires_at IS NULL OR h.expires_at > now()))
			  AND NOT EXISTS (SELECT 1 FROM legal_holds h WHERE h.subject_type='user' AND h.subject_id=p.user_id::text
			    AND (h.expires_at IS NULL OR h.expires_at > now()))
			ORDER BY p.id LIMIT 100`, cutoff)
		if err != nil {
			_ = tx.Rollback(ctx)
			return archived, purged, err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				_ = tx.Rollback(ctx)
				return archived, purged, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			_ = tx.Rollback(ctx)
			return archived, purged, err
		}
		if len(ids) == 0 {
			_ = tx.Commit(ctx)
			break
		}
		for _, id := range ids {
			payload, err := paymentArchivePayload(ctx, tx, id)
			if err != nil {
				_ = tx.Rollback(ctx)
				return archived, purged, err
			}
			if err := s.appendArchive(ctx, tx, "payment", id.String(), payload); err != nil {
				_ = tx.Rollback(ctx)
				return archived, purged, err
			}
		}
		tag, err := tx.Exec(ctx, `DELETE FROM payments WHERE id = ANY($1)`, ids)
		if err != nil {
			_ = tx.Rollback(ctx)
			return archived, purged, err
		}
		if err := tx.Commit(ctx); err != nil {
			return archived, purged, err
		}
		archived += int64(len(ids))
		purged += tag.RowsAffected()
	}
	return archived, purged, nil
}

// archiveAndPurgeInvoices snapshots invoices (items + payments) before deleting.
func (s *Store) archiveAndPurgeInvoices(ctx context.Context, cutoff time.Time) (int64, int64, error) {
	var archived, purged int64
	for {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return archived, purged, err
		}
		rows, err := tx.Query(ctx, `
			SELECT i.id FROM invoices i
			WHERE i.created_at < $1
			  AND NOT EXISTS (SELECT 1 FROM legal_holds h WHERE h.subject_type='invoice' AND h.subject_id=i.id::text
			    AND (h.expires_at IS NULL OR h.expires_at > now()))
			  AND NOT EXISTS (SELECT 1 FROM legal_holds h WHERE h.subject_type='user' AND h.subject_id=i.created_by_user_id::text
			    AND (h.expires_at IS NULL OR h.expires_at > now()))
			ORDER BY i.id LIMIT 100`, cutoff)
		if err != nil {
			_ = tx.Rollback(ctx)
			return archived, purged, err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				_ = tx.Rollback(ctx)
				return archived, purged, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			_ = tx.Rollback(ctx)
			return archived, purged, err
		}
		if len(ids) == 0 {
			_ = tx.Commit(ctx)
			break
		}
		for _, id := range ids {
			payload, err := invoiceArchivePayload(ctx, tx, id)
			if err != nil {
				_ = tx.Rollback(ctx)
				return archived, purged, err
			}
			if err := s.appendArchive(ctx, tx, "invoice", id.String(), payload); err != nil {
				_ = tx.Rollback(ctx)
				return archived, purged, err
			}
		}
		tag, err := tx.Exec(ctx, `DELETE FROM invoices WHERE id = ANY($1)`, ids)
		if err != nil {
			_ = tx.Rollback(ctx)
			return archived, purged, err
		}
		if err := tx.Commit(ctx); err != nil {
			return archived, purged, err
		}
		archived += int64(len(ids))
		purged += tag.RowsAffected()
	}
	return archived, purged, nil
}

// archiveAndPurgeThrift archives completed/cancelled thrift groups with their
// cycles, memberships, contributions and payouts, then deletes them.
func (s *Store) archiveAndPurgeThrift(ctx context.Context, cutoff time.Time) (int64, error) {
	var purged int64
	for {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return purged, err
		}
		rows, err := tx.Query(ctx, `
			SELECT tg.id FROM thrift_groups tg
			WHERE tg.status IN ('completed','cancelled')
			  AND tg.updated_at < $1
			  AND NOT EXISTS (SELECT 1 FROM legal_holds h WHERE h.subject_type='thrift_group' AND h.subject_id=tg.id::text
			    AND (h.expires_at IS NULL OR h.expires_at > now()))
			  AND NOT EXISTS (SELECT 1 FROM legal_holds h WHERE h.subject_type='user' AND h.subject_id=tg.creator_user_id::text
			    AND (h.expires_at IS NULL OR h.expires_at > now()))
			ORDER BY tg.id LIMIT 100`, cutoff)
		if err != nil {
			_ = tx.Rollback(ctx)
			return purged, err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				_ = tx.Rollback(ctx)
				return purged, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			_ = tx.Rollback(ctx)
			return purged, err
		}
		if len(ids) == 0 {
			_ = tx.Commit(ctx)
			break
		}
		for _, id := range ids {
			payload, err := thriftArchivePayload(ctx, tx, id)
			if err != nil {
				_ = tx.Rollback(ctx)
				return purged, err
			}
			if err := s.appendArchive(ctx, tx, "thrift_group", id.String(), payload); err != nil {
				_ = tx.Rollback(ctx)
				return purged, err
			}
		}
		tag, err := tx.Exec(ctx, `DELETE FROM thrift_groups WHERE id = ANY($1)`, ids)
		if err != nil {
			_ = tx.Rollback(ctx)
			return purged, err
		}
		if err := tx.Commit(ctx); err != nil {
			return purged, err
		}
		purged += tag.RowsAffected()
	}
	return purged, nil
}

// archiveAndPurgeAudit snapshots audit rows older than the cutoff into the
// ledger, then purges them from the live (append-only) table. The append-only
// trigger is briefly disabled for the purge and re-enabled afterwards; the new
// head row is re-rooted so the retained chain still verifies.
func (s *Store) archiveAndPurgeAudit(ctx context.Context, cutoff time.Time) (int64, int64, error) {
	var archived, purged int64
	for {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return archived, purged, err
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE audit_logs DISABLE TRIGGER ALL`); err != nil {
			_ = tx.Rollback(ctx)
			return archived, purged, err
		}
		rows, err := tx.Query(ctx, `
			SELECT id FROM audit_logs
			WHERE occurred_at < $1
			ORDER BY id LIMIT 500`, cutoff)
		if err != nil {
			_ = tx.Rollback(ctx)
			return archived, purged, err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				_ = tx.Rollback(ctx)
				return archived, purged, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			_ = tx.Rollback(ctx)
			return archived, purged, err
		}
		if len(ids) == 0 {
			if _, err := tx.Exec(ctx, `ALTER TABLE audit_logs ENABLE TRIGGER ALL`); err != nil {
				_ = tx.Rollback(ctx)
				return archived, purged, err
			}
			_ = tx.Commit(ctx)
			break
		}
		for _, id := range ids {
			payload, err := auditArchivePayload(ctx, tx, id)
			if err != nil {
				_ = tx.Rollback(ctx)
				return archived, purged, err
			}
			if err := s.appendArchive(ctx, tx, "audit", fmt.Sprintf("%d", id), payload); err != nil {
				_ = tx.Rollback(ctx)
				return archived, purged, err
			}
		}
		tag, err := tx.Exec(ctx, `DELETE FROM audit_logs WHERE id = ANY($1)`, ids)
		if err != nil {
			_ = tx.Rollback(ctx)
			return archived, purged, err
		}
		if err := rerootAuditChain(ctx, tx); err != nil {
			_ = tx.Rollback(ctx)
			return archived, purged, err
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE audit_logs ENABLE TRIGGER ALL`); err != nil {
			_ = tx.Rollback(ctx)
			return archived, purged, err
		}
		if err := tx.Commit(ctx); err != nil {
			return archived, purged, err
		}
		archived += int64(len(ids))
		purged += tag.RowsAffected()
	}
	return archived, purged, nil
}

// rerootAuditChain points the first remaining audit row at an empty predecessor
// so VerifyAuditChain still passes after the oldest entries were purged.
func rerootAuditChain(ctx context.Context, tx pgx.Tx) error {
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM audit_logs ORDER BY id ASC LIMIT 1`).Scan(&id)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var entry AuditLog
	var details []byte
	if err := tx.QueryRow(ctx, `
		SELECT occurred_at, actor_type, COALESCE(actor_id::text,''), COALESCE(actor_email,''),
		       COALESCE(ip::text,''), action, COALESCE(resource_type,''), COALESCE(resource_id,''),
		       details
		FROM audit_logs WHERE id=$1`, id).Scan(
		&entry.OccurredAt, &entry.ActorType, &entry.ActorID.UUID, &entry.ActorEmail.String,
		&entry.IP.String, &entry.Action, &entry.ResourceType.String, &entry.ResourceID.String,
		&details); err != nil {
		return err
	}
	_ = json.Unmarshal(details, &entry.Details)
	entry.ActorID.Valid = entry.ActorID.UUID != uuid.Nil
	entry.ActorEmail.Valid = entry.ActorEmail.String != ""
	entry.IP.Valid = entry.IP.String != ""
	entry.ResourceType.Valid = entry.ResourceType.String != ""
	entry.ResourceID.Valid = entry.ResourceID.String != ""
	hash := auditHash("", entry)
	_, err = tx.Exec(ctx, `UPDATE audit_logs SET prev_hash='', hash=$2 WHERE id=$1`, id, hash)
	return err
}

func paymentArchivePayload(ctx context.Context, tx pgx.Tx, id uuid.UUID) ([]byte, error) {
	var p domain.Payment
	var paidAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT id, user_id, merchant_id, amount_kobo, currency, status, provider,
		       provider_reference, channel, recipient, checkout_url, checkout_token, receipt_token,
		       merchant_reference, metadata,
		       failure_reason, created_at, updated_at, paid_at
		FROM payments WHERE id=$1`, id).Scan(
		&p.ID, &p.UserID, &p.MerchantID, &p.AmountKobo, &p.Currency, &p.Status, &p.Provider,
		&p.ProviderReference, &p.Channel, &p.Recipient, &p.CheckoutURL, &p.CheckoutToken, &p.ReceiptToken,
		&p.MerchantReference, &p.Metadata,
		&p.FailureReason, &p.CreatedAt, &p.UpdatedAt, &paidAt)
	if err != nil {
		return nil, err
	}
	if paidAt != nil {
		p.PaidAt = paidAt
	}
	events, err := paymentEventsPayload(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"payment": p, "events": events})
}

func paymentEventsPayload(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `
		SELECT from_status, to_status, source, detail, created_at
		FROM payment_events WHERE payment_id=$1 ORDER BY id`, paymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []map[string]any
	for rows.Next() {
		var from, to, source string
		var detail []byte
		var createdAt time.Time
		if err := rows.Scan(&from, &to, &source, &detail, &createdAt); err != nil {
			return nil, err
		}
		var details map[string]any
		_ = json.Unmarshal(detail, &details)
		events = append(events, map[string]any{
			"from_status": from, "to_status": to, "source": source, "detail": details, "created_at": createdAt,
		})
	}
	return events, rows.Err()
}

func invoiceArchivePayload(ctx context.Context, tx pgx.Tx, id uuid.UUID) ([]byte, error) {
	var (
		invoiceID, merchantID, createdBy uuid.UUID
		customerPhone, customerEmail     string
		reference, status                string
		deliveryFee, subtotal, total     int64
		amountPaid                       int64
		dueAt, paidAt                    *time.Time
		createdAt, updatedAt             time.Time
	)
	if err := tx.QueryRow(ctx, `
		SELECT id, merchant_id, created_by_user_id, customer_whatsapp_number, customer_email,
		       reference, status, delivery_fee_kobo, subtotal_kobo, total_kobo, amount_paid_kobo,
		       due_at, created_at, updated_at, paid_at
		FROM invoices WHERE id=$1`, id).Scan(
		&invoiceID, &merchantID, &createdBy, &customerPhone, &customerEmail,
		&reference, &status, &deliveryFee, &subtotal, &total, &amountPaid,
		&dueAt, &createdAt, &updatedAt, &paidAt); err != nil {
		return nil, err
	}
	items, err := invoiceItemsPayload(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	payments, err := invoicePaymentsPayload(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"invoice": map[string]any{
			"id": invoiceID, "merchant_id": merchantID, "created_by_user_id": createdBy,
			"customer_whatsapp_number": customerPhone, "customer_email": customerEmail,
			"reference": reference, "status": status, "delivery_fee_kobo": deliveryFee,
			"subtotal_kobo": subtotal, "total_kobo": total, "amount_paid_kobo": amountPaid,
			"due_at": dueAt, "created_at": createdAt, "updated_at": updatedAt, "paid_at": paidAt,
		},
		"items": items, "invoice_payments": payments,
	})
}

func invoiceItemsPayload(ctx context.Context, tx pgx.Tx, invoiceID uuid.UUID) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `
		SELECT description, quantity, unit_price_kobo, line_total_kobo, sort_order
		FROM invoice_items WHERE invoice_id=$1 ORDER BY sort_order`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []map[string]any
	for rows.Next() {
		var description string
		var quantity, unitPrice, lineTotal int64
		var sortOrder int
		if err := rows.Scan(&description, &quantity, &unitPrice, &lineTotal, &sortOrder); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"description": description, "quantity": quantity,
			"unit_price_kobo": unitPrice, "line_total_kobo": lineTotal, "sort_order": sortOrder,
		})
	}
	return items, rows.Err()
}

func invoicePaymentsPayload(ctx context.Context, tx pgx.Tx, invoiceID uuid.UUID) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, payment_id, payer_user_id, amount_kobo, status, created_at, updated_at
		FROM invoice_payments WHERE invoice_id=$1 ORDER BY created_at`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var payments []map[string]any
	for rows.Next() {
		var id, paymentID, payerID uuid.UUID
		var amount int64
		var status string
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &paymentID, &payerID, &amount, &status, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		payments = append(payments, map[string]any{
			"id": id, "payment_id": paymentID, "payer_user_id": payerID,
			"amount_kobo": amount, "status": status, "created_at": createdAt, "updated_at": updatedAt,
		})
	}
	return payments, rows.Err()
}

func thriftArchivePayload(ctx context.Context, tx pgx.Tx, groupID uuid.UUID) ([]byte, error) {
	var (
		id, creatorUserID uuid.UUID
		name              string
		amountKobo        int64
		frequency         string
		targetMembers     int
		inviteCode        string
		status            string
		currentCycle      int
		activatedAt       *time.Time
		completedAt       *time.Time
		cancelledAt       *time.Time
		createdAt         time.Time
		updatedAt         time.Time
	)
	if err := tx.QueryRow(ctx, `
		SELECT id, creator_user_id, name, contribution_amount_kobo, frequency, target_member_count,
		       invite_code, status, current_cycle, created_at, updated_at, activated_at, completed_at, cancelled_at
		FROM thrift_groups WHERE id=$1`, groupID).Scan(
		&id, &creatorUserID, &name, &amountKobo, &frequency, &targetMembers,
		&inviteCode, &status, &currentCycle, &createdAt, &updatedAt, &activatedAt, &completedAt, &cancelledAt); err != nil {
		return nil, err
	}
	members, err := thriftMembersPayload(ctx, tx, groupID)
	if err != nil {
		return nil, err
	}
	cycles, err := thriftCyclesPayload(ctx, tx, groupID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"thrift_group": map[string]any{
			"id": id, "creator_user_id": creatorUserID, "name": name,
			"contribution_amount_kobo": amountKobo, "frequency": frequency, "target_member_count": targetMembers,
			"invite_code": inviteCode, "status": status, "current_cycle": currentCycle,
			"created_at": createdAt, "updated_at": updatedAt,
			"activated_at": activatedAt, "completed_at": completedAt, "cancelled_at": cancelledAt,
		},
		"members": members,
		"cycles":  cycles,
	})
}

func thriftMembersPayload(ctx context.Context, tx pgx.Tx, groupID uuid.UUID) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, user_id, status, payout_position, joined_at, confirmed_at, removed_at
		FROM thrift_members WHERE group_id=$1 ORDER BY joined_at`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []map[string]any
	for rows.Next() {
		var id, userID uuid.UUID
		var status string
		var position *int
		var joinedAt, confirmedAt time.Time
		var removedAt *time.Time
		if err := rows.Scan(&id, &userID, &status, &position, &joinedAt, &confirmedAt, &removedAt); err != nil {
			return nil, err
		}
		members = append(members, map[string]any{
			"id": id, "user_id": userID, "status": status, "payout_position": position,
			"joined_at": joinedAt, "confirmed_at": confirmedAt, "removed_at": removedAt,
		})
	}
	return members, rows.Err()
}

func thriftCyclesPayload(ctx context.Context, tx pgx.Tx, groupID uuid.UUID) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.id, c.cycle_number, c.due_at, c.payout_member_id, c.status,
		       (SELECT json_agg(cc) FROM (
		           SELECT id, member_id, payment_id, amount_kobo, status, paid_at, created_at, updated_at
		           FROM thrift_contributions WHERE cycle_id=c.id
		         ) cc),
		       (SELECT json_agg(pp) FROM (
		           SELECT id, payout_member_id, amount_kobo, status, completed_at, created_at, updated_at
		           FROM thrift_payouts WHERE cycle_id=c.id
		         ) pp)
		FROM thrift_cycles c WHERE c.group_id=$1 ORDER BY c.cycle_number`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cycles []map[string]any
	for rows.Next() {
		var id, payoutMemberID uuid.UUID
		var cycleNumber int
		var dueAt time.Time
		var status string
		var contributions, payouts []byte
		if err := rows.Scan(&id, &cycleNumber, &dueAt, &payoutMemberID, &status, &contributions, &payouts); err != nil {
			return nil, err
		}
		var contributionsAny, payoutsAny any
		if contributions != nil {
			_ = json.Unmarshal(contributions, &contributionsAny)
		}
		if payouts != nil {
			_ = json.Unmarshal(payouts, &payoutsAny)
		}
		cycles = append(cycles, map[string]any{
			"id": id, "cycle_number": cycleNumber, "due_at": dueAt,
			"payout_member_id": payoutMemberID, "status": status,
			"contributions": contributionsAny, "payouts": payoutsAny,
		})
	}
	return cycles, rows.Err()
}

func auditArchivePayload(ctx context.Context, tx pgx.Tx, id int64) ([]byte, error) {
	var entry AuditLog
	var details []byte
	err := tx.QueryRow(ctx, `
		SELECT occurred_at, actor_type, COALESCE(actor_id::text,''), COALESCE(actor_email,''),
		       COALESCE(ip::text,''), action, COALESCE(resource_type,''), COALESCE(resource_id,''),
		       details, prev_hash, hash
		FROM audit_logs WHERE id=$1`, id).Scan(
		&entry.OccurredAt, &entry.ActorType, &entry.ActorID.UUID, &entry.ActorEmail.String,
		&entry.IP.String, &entry.Action, &entry.ResourceType.String, &entry.ResourceID.String,
		&details, &entry.PrevHash, &entry.Hash)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(details, &entry.Details)
	entry.ActorID.Valid = entry.ActorID.UUID != uuid.Nil
	entry.ActorEmail.Valid = entry.ActorEmail.String != ""
	entry.IP.Valid = entry.IP.String != ""
	entry.ResourceType.Valid = entry.ResourceType.String != ""
	entry.ResourceID.Valid = entry.ResourceID.String != ""
	return json.Marshal(entry)
}
