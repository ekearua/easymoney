package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// PhoneDigest hashes the normalized phone number so account-link codes are
// bound to the number the customer claims to own without storing the number
// itself in the one-time-code table.
func PhoneDigest(phone string) string {
	var digits strings.Builder
	for _, r := range strings.TrimSpace(phone) {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	sum := sha256.Sum256([]byte(digits.String()))
	return hex.EncodeToString(sum[:])
}

// CreateLinkRequest stores a hashed one-time code that proves the caller owns
// a WhatsApp number. Previous unconsumed codes for the same user and channel
// are invalidated; a resend within the cooldown window is refused.
func (s *Store) CreateLinkRequest(ctx context.Context, userID uuid.UUID, channel, phone string, codeHash []byte, expiresAt time.Time) error {
	cooldown := s.EmailResendCooldown
	if cooldown <= 0 {
		cooldown = defaultEmailResendCooldown
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var recent bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM user_link_requests
			WHERE user_id=$1 AND channel=$2 AND consumed_at IS NULL
			  AND created_at > now() - $3::interval
		)`, userID, channel, cooldown).Scan(&recent)
	if err != nil {
		return err
	}
	if recent {
		return ErrResendTooSoon
	}
	if _, err := tx.Exec(ctx, `
		UPDATE user_link_requests SET consumed_at=now()
		WHERE user_id=$1 AND channel=$2 AND consumed_at IS NULL`, userID, channel); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO user_link_requests(user_id, channel, phone_digest, code_hash, expires_at)
		VALUES($1,$2,$3,$4,$5)`, userID, channel, PhoneDigest(phone), codeHash, expiresAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// VerifyLinkRequest consumes the latest valid code when the submitted
// candidate matches the stored bcrypt hash. Mismatches increment attempts so
// repeated guessing is bounded. On success the caller must then merge rows.
func (s *Store) VerifyLinkRequest(ctx context.Context, userID uuid.UUID, channel, phone string, candidate []byte) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var id uuid.UUID
	var stored []byte
	var attempts int
	err = tx.QueryRow(ctx, `
		SELECT id, code_hash, attempts
		FROM user_link_requests
		WHERE user_id=$1 AND channel=$2 AND phone_digest=$3
		  AND consumed_at IS NULL AND expires_at > now()
		ORDER BY created_at DESC
		LIMIT 1
		FOR UPDATE`, userID, channel, PhoneDigest(phone)).Scan(&id, &stored, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if attempts >= 5 || bcrypt.CompareHashAndPassword(stored, candidate) != nil {
		_, err = tx.Exec(ctx, `UPDATE user_link_requests SET attempts=attempts+1 WHERE id=$1`, id)
		if err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE user_link_requests SET consumed_at=now() WHERE id=$1`, id); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// linkChannelStatement mirrors LinkChannelToUser's per-channel UPDATE so it
// can run inside the merge transaction.
func linkChannelStatement(channel string) (string, bool) {
	switch channel {
	case "whatsapp":
		return `
			UPDATE users SET
				whatsapp_number=$2,
				whatsapp_verified_at=COALESCE(whatsapp_verified_at, now()),
				number_confirmed_at=COALESCE(number_confirmed_at, now()),
				onboarding_complete=true,
				updated_at=now()
			WHERE id=$1`, true
	case "instagram":
		return `
			UPDATE users SET
				instagram_igsid=$2,
				instagram_username=CASE WHEN $3<>'' THEN $3 ELSE instagram_username END,
				instagram_verified_at=COALESCE(instagram_verified_at, now()),
				instagram_confirmed_at=COALESCE(instagram_confirmed_at, now()),
				onboarding_complete=true,
				updated_at=now()
			WHERE id=$1`, true
	case "tiktok":
		return `
			UPDATE users SET
				tiktok_open_id=$2,
				tiktok_username=CASE WHEN $3<>'' THEN $3 ELSE tiktok_username END,
				tiktok_verified_at=COALESCE(tiktok_verified_at, now()),
				tiktok_confirmed_at=COALESCE(tiktok_confirmed_at, now()),
				onboarding_complete=true,
				updated_at=now()
			WHERE id=$1`, true
	case "telegram":
		return `
			UPDATE users SET
				telegram_chat_id=$2,
				telegram_username=CASE WHEN $3<>'' THEN $3 ELSE telegram_username END,
				telegram_verified_at=COALESCE(telegram_verified_at, now()),
				telegram_confirmed_at=COALESCE(telegram_confirmed_at, now()),
				onboarding_complete=true,
				updated_at=now()
			WHERE id=$1`, true
	}
	return "", false
}

// clearChannelHandleStatement clears the owning handle column on the tombstone
// row so the same handle uniquely transfers to the surviving account.
func clearChannelHandleStatement(channel string) (string, bool) {
	switch channel {
	case "whatsapp":
		return `UPDATE users SET whatsapp_number=NULL, updated_at=now() WHERE id=$1`, true
	case "instagram":
		return `UPDATE users SET instagram_igsid=NULL, instagram_username='', updated_at=now() WHERE id=$1`, true
	case "tiktok":
		return `UPDATE users SET tiktok_open_id=NULL, tiktok_union_id=NULL, tiktok_username='', updated_at=now() WHERE id=$1`, true
	case "telegram":
		return `UPDATE users SET telegram_chat_id=NULL, telegram_user_id=NULL, telegram_username='', updated_at=now() WHERE id=$1`, true
	}
	return "", false
}

// MergeUsers folds the source channel row into the surviving primary account:
// payments, sessions, and wallets are reparented, then the channel handle moves
// onto the primary and the source row becomes a tombstone pointing at it. The
// operation is serialized per pair so concurrent links cannot interleave.
func (s *Store) MergeUsers(ctx context.Context, sourceID, targetID uuid.UUID, channel, handle, username string) error {
	if sourceID == targetID {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	lockKey := sourceID.String() + ":" + targetID.String()
	if targetID.String() < sourceID.String() {
		lockKey = targetID.String() + ":" + sourceID.String()
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "xego:merge:"+lockKey); err != nil {
		return err
	}

	// Reparent every row keyed on the source across the schema (tombstone
	// merge keeps the surviving account's data when both sides have a row).
	rows, err := tx.Query(ctx, `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema='public'
		  AND column_name IN ('user_id','payer_user_id','recipient_user_id','created_by_user_id')
		  AND table_name <> 'users'
		  AND table_name <> 'user_link_requests'
		ORDER BY table_name, column_name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type ref struct{ table, column string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.table, &r.column); err != nil {
			return err
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range refs {
		stmt := fmt.Sprintf(
			"UPDATE %s SET %s=$2 WHERE %s=$1",
			pgx.Identifier{r.table}.Sanitize(),
			pgx.Identifier{r.column}.Sanitize(),
			pgx.Identifier{r.column}.Sanitize())
		_, err := tx.Exec(ctx, stmt, sourceID, targetID)
		if err == nil {
			continue
		}
		if !strings.Contains(err.Error(), "duplicate key") {
			return err
		}
		// Unique per-user row already exists on the primary: keep the primary
		// (prefers richer/later account) and drop the source's duplicate.
		del := fmt.Sprintf("DELETE FROM %s WHERE %s=$1",
			pgx.Identifier{r.table}.Sanitize(),
			pgx.Identifier{r.column}.Sanitize())
		if _, err := tx.Exec(ctx, del, sourceID); err != nil {
			return err
		}
	}

	// The channel handle moves to the primary. Clear it from the tombstone
	// first so the transfer cannot violate its unique constraint, then attach
	// it to the surviving row and stamp the channel as confirmed.
	if clearStmt, ok := clearChannelHandleStatement(channel); ok {
		if _, err := tx.Exec(ctx, clearStmt, sourceID); err != nil {
			return err
		}
	}
	if linkStmt, ok := linkChannelStatement(channel); ok {
		if _, err := tx.Exec(ctx, linkStmt, targetID, handle, username); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE users SET merged_into_id=$2, updated_at=now() WHERE id=$1`, sourceID, targetID); err != nil {
		return err
	}
	// Carry over profile fields the primary is missing so the linked-in channel
	// still has a name/email usable by receipts and flows.
	if _, err := tx.Exec(ctx, `
		UPDATE users SET
			display_name=COALESCE(NULLIF(display_name,''), (SELECT display_name FROM users WHERE id=$1)),
			email=COALESCE(NULLIF(email,''), (SELECT email FROM users WHERE id=$1)),
			updated_at=now()
		WHERE id=$2`, sourceID, targetID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
