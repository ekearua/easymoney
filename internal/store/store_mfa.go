package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Second-factor kinds. TOTP is an authenticator app, Email a one-time code, and
// WebAuthn a passkey bound to a device.
const (
	MFAKindTOTP    = "totp"
	MFAKindEmail   = "email"
	MFAKindPasskey = "webauthn"
)

// Second-factor scopes: the two portals that share the two-step login flow.
const (
	MFAScopeAdmin    = "admin"
	MFAScopeMerchant = "merchant"
)

// MFAFactor is one enrolled second factor. SecretCipher is the AES-256-GCM
// ciphertext of a TOTP shared secret; a passkey's credential id and public key
// are not secrets and are stored as-is.
type MFAFactor struct {
	ID           int64
	Scope        string
	SubjectID    uuid.UUID
	Kind         string
	Label        string
	SecretCipher []byte
	CredentialID []byte
	PublicKey    []byte
	SignCount    int64
	Transports   string
	ConfirmedAt  *time.Time
	LastUsedAt   *time.Time
	CreatedAt    time.Time
}

// MFAChallenge is one in-flight second-factor step.
type MFAChallenge struct {
	Scope         string
	SubjectID     uuid.UUID
	Method        string
	FactorID      *int64
	SecretCipher  []byte
	CodeHash      []byte
	WebAuthnState []byte
	Attempts      int
	ExpiresAt     time.Time
}

// KindLabel is the operator-facing name of a factor kind.
func MFAKindLabel(kind string) string {
	switch kind {
	case MFAKindTOTP:
		return "Authenticator app"
	case MFAKindEmail:
		return "Email code"
	case MFAKindPasskey:
		return "Passkey"
	}
	return kind
}

const mfaFactorColumns = `id, scope, subject_id, kind, label, secret_cipher, credential_id,
	public_key, sign_count, transports, confirmed_at, last_used_at, created_at`

func scanMFAFactor(row pgx.Row) (MFAFactor, error) {
	var factor MFAFactor
	err := row.Scan(&factor.ID, &factor.Scope, &factor.SubjectID, &factor.Kind, &factor.Label,
		&factor.SecretCipher, &factor.CredentialID, &factor.PublicKey, &factor.SignCount,
		&factor.Transports, &factor.ConfirmedAt, &factor.LastUsedAt, &factor.CreatedAt)
	return factor, err
}

// MFAFactors lists a subject's usable factors, oldest first. Pending factors
// (confirmed_at IS NULL) are included: an enrollment that has not been verified
// yet is still the subject's step, and the security page labels it.
func (s *Store) MFAFactors(ctx context.Context, scope string, subjectID uuid.UUID) ([]MFAFactor, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+mfaFactorColumns+` FROM mfa_factors
		WHERE scope=$1 AND subject_id=$2 AND disabled_at IS NULL
		ORDER BY created_at ASC, id ASC`, scope, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var factors []MFAFactor
	for rows.Next() {
		factor, err := scanMFAFactor(rows)
		if err != nil {
			return nil, err
		}
		factors = append(factors, factor)
	}
	return factors, rows.Err()
}

// MFAFactorByKind returns the single factor of a kind, or nil when the subject
// has none.
func (s *Store) MFAFactorByKind(ctx context.Context, scope string, subjectID uuid.UUID, kind string) (*MFAFactor, error) {
	factor, err := scanMFAFactor(s.pool.QueryRow(ctx, `
		SELECT `+mfaFactorColumns+` FROM mfa_factors
		WHERE scope=$1 AND subject_id=$2 AND kind=$3 AND disabled_at IS NULL`, scope, subjectID, kind))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &factor, nil
}

// MFAFactorByID reads one factor by id.
func (s *Store) MFAFactorByID(ctx context.Context, id int64) (*MFAFactor, error) {
	factor, err := scanMFAFactor(s.pool.QueryRow(ctx, `
		SELECT `+mfaFactorColumns+` FROM mfa_factors WHERE id=$1 AND disabled_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &factor, nil
}

// MFAFactorByCredential resolves a passkey from the credential id a browser
// presents during an assertion.
func (s *Store) MFAFactorByCredential(ctx context.Context, credentialID []byte) (*MFAFactor, error) {
	factor, err := scanMFAFactor(s.pool.QueryRow(ctx, `
		SELECT `+mfaFactorColumns+` FROM mfa_factors
		WHERE credential_id=$1 AND disabled_at IS NULL`, credentialID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &factor, nil
}

// UpsertMFAFactor enrolls or replaces the single factor of a kind and returns
// its id. Only TOTP and email are one-per-subject; passkeys are registered one
// credential at a time via InsertPasskeyFactor.
func (s *Store) UpsertMFAFactor(ctx context.Context, factor MFAFactor) (int64, error) {
	var conflict string
	switch factor.Kind {
	case MFAKindTOTP:
		conflict = `ON CONFLICT (scope, subject_id) WHERE kind='totp'`
	case MFAKindEmail:
		conflict = `ON CONFLICT (scope, subject_id) WHERE kind='email'`
	default:
		return 0, fmt.Errorf("factor kind %q is not single-per-subject", factor.Kind)
	}
	label := factor.Label
	if label == "" {
		label = MFAKindLabel(factor.Kind)
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO mfa_factors(scope, subject_id, kind, label, secret_cipher, confirmed_at)
		VALUES($1,$2,$3,$4,$5,$6)
		`+conflict+`
		DO UPDATE SET label=EXCLUDED.label, secret_cipher=EXCLUDED.secret_cipher,
		              confirmed_at=COALESCE(EXCLUDED.confirmed_at, mfa_factors.confirmed_at),
		              disabled_at=NULL, updated_at=now()
		RETURNING id`,
		factor.Scope, factor.SubjectID, factor.Kind, label, factor.SecretCipher, factor.ConfirmedAt).Scan(&id)
	return id, err
}

// InsertPasskeyFactor stores a newly registered passkey credential.
func (s *Store) InsertPasskeyFactor(ctx context.Context, factor MFAFactor) (int64, error) {
	label := factor.Label
	if label == "" {
		label = MFAKindLabel(MFAKindPasskey)
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO mfa_factors(scope, subject_id, kind, label, credential_id, public_key,
		                        sign_count, transports, confirmed_at)
		VALUES($1,$2,'webauthn',$3,$4,$5,$6,$7,$8)
		RETURNING id`,
		factor.Scope, factor.SubjectID, label, factor.CredentialID, factor.PublicKey,
		factor.SignCount, factor.Transports, factor.ConfirmedAt).Scan(&id)
	return id, err
}

// ConfirmMFAFactor marks a factor usable after its first successful challenge.
func (s *Store) ConfirmMFAFactor(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE mfa_factors SET confirmed_at=COALESCE(confirmed_at, now()), updated_at=now()
		WHERE id=$1`, id)
	return err
}

// TouchMFAFactor records a successful use of a factor.
func (s *Store) TouchMFAFactor(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE mfa_factors SET last_used_at=now(), confirmed_at=COALESCE(confirmed_at, now()), updated_at=now()
		WHERE id=$1`, id)
	return err
}

// UpdatePasskeySignCount stores the newest signature counter, which must grow
// on every assertion (a reused or rewound counter means a cloned credential).
func (s *Store) UpdatePasskeySignCount(ctx context.Context, id int64, signCount int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE mfa_factors SET sign_count=$2, updated_at=now() WHERE id=$1`, id, signCount)
	return err
}

// RenameMFAFactor relabels a factor the subject owns.
func (s *Store) RenameMFAFactor(ctx context.Context, scope string, subjectID uuid.UUID, id int64, label string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE mfa_factors SET label=$4, updated_at=now()
		WHERE id=$3 AND scope=$1 AND subject_id=$2`, scope, subjectID, id, label)
	return err
}

// DeleteMFAFactor revokes one factor the subject owns.
func (s *Store) DeleteMFAFactor(ctx context.Context, scope string, subjectID uuid.UUID, id int64) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM mfa_factors WHERE id=$3 AND scope=$1 AND subject_id=$2`, scope, subjectID, id)
	return err
}

// DeleteMFAFactorsForSubject revokes every factor a subject has enrolled. It
// backs the operator-side reset for an account that lost its devices.
func (s *Store) DeleteMFAFactorsForSubject(ctx context.Context, scope string, subjectID uuid.UUID) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM mfa_factors WHERE scope=$1 AND subject_id=$2`, scope, subjectID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// MFAFactorCount counts a subject's factors, optionally only confirmed ones.
func (s *Store) MFAFactorCount(ctx context.Context, scope string, subjectID uuid.UUID) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM mfa_factors
		WHERE scope=$1 AND subject_id=$2 AND disabled_at IS NULL`, scope, subjectID).Scan(&count)
	return count, err
}

// CreateMFAChallenge stores a pending second-factor step keyed by the SHA-256
// hash of its single-use token.
func (s *Store) CreateMFAChallenge(ctx context.Context, tokenHash []byte, challenge MFAChallenge) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO mfa_challenges(token_hash, scope, subject_id, method, factor_id,
		                           secret_cipher, code_hash, webauthn_state, expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		tokenHash, challenge.Scope, challenge.SubjectID, challenge.Method, challenge.FactorID,
		challenge.SecretCipher, challenge.CodeHash, challenge.WebAuthnState, challenge.ExpiresAt)
	return err
}

// MFAChallengeByToken reads a live step without consuming it, so a refreshed
// page still renders the step and a mistyped code can be retried.
func (s *Store) MFAChallengeByToken(ctx context.Context, tokenHash []byte) (MFAChallenge, bool, error) {
	var challenge MFAChallenge
	err := s.pool.QueryRow(ctx, `
		SELECT scope, subject_id, method, factor_id, secret_cipher, code_hash,
		       webauthn_state, attempts, expires_at
		FROM mfa_challenges
		WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at > now()`, tokenHash).Scan(
		&challenge.Scope, &challenge.SubjectID, &challenge.Method, &challenge.FactorID,
		&challenge.SecretCipher, &challenge.CodeHash, &challenge.WebAuthnState,
		&challenge.Attempts, &challenge.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MFAChallenge{}, false, nil
	}
	if err != nil {
		return MFAChallenge{}, false, err
	}
	return challenge, true, nil
}

// FailMFAChallenge records a wrong code or assertion and deletes the step once
// the attempt budget is spent, returning the attempts left.
func (s *Store) FailMFAChallenge(ctx context.Context, tokenHash []byte, maxAttempts int) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var attempts int
	err = tx.QueryRow(ctx, `
		UPDATE mfa_challenges SET attempts=attempts+1
		WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at > now()
		RETURNING attempts`, tokenHash).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, tx.Commit(ctx)
	}
	if err != nil {
		return 0, err
	}
	remaining := maxAttempts - attempts
	if remaining <= 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM mfa_challenges WHERE token_hash=$1`, tokenHash); err != nil {
			return 0, err
		}
		remaining = 0
	}
	return remaining, tx.Commit(ctx)
}

// SetMFAChallengeCode replaces the code hash of a live step. A resend of an
// emailed code needs a fresh hash; the attempt counter deliberately survives,
// so repeatedly asking for a new code cannot farm fresh guesses.
func (s *Store) SetMFAChallengeCode(ctx context.Context, tokenHash []byte, codeHash []byte) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE mfa_challenges SET code_hash=$2
		WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at > now()`,
		tokenHash, codeHash)
	return err
}

// SwitchMFAChallenge points a live step at another enrolled method. The
// attempt counter survives the switch for the same reason as a resend.
func (s *Store) SwitchMFAChallenge(ctx context.Context, tokenHash []byte, method string, factorID *int64, secretCipher, codeHash, webauthnState []byte) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE mfa_challenges SET method=$2, factor_id=$3, secret_cipher=$4, code_hash=$5, webauthn_state=$6
		WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at > now()`,
		tokenHash, method, factorID, secretCipher, codeHash, webauthnState)
	return err
}

// ConsumeMFAChallenge deletes and returns a live step in one transaction.
func (s *Store) ConsumeMFAChallenge(ctx context.Context, tokenHash []byte) (MFAChallenge, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MFAChallenge{}, false, err
	}
	defer tx.Rollback(ctx)
	var challenge MFAChallenge
	err = tx.QueryRow(ctx, `
		SELECT scope, subject_id, method, factor_id, secret_cipher, code_hash,
		       webauthn_state, attempts, expires_at
		FROM mfa_challenges
		WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at > now()
		FOR UPDATE`, tokenHash).Scan(
		&challenge.Scope, &challenge.SubjectID, &challenge.Method, &challenge.FactorID,
		&challenge.SecretCipher, &challenge.CodeHash, &challenge.WebAuthnState,
		&challenge.Attempts, &challenge.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MFAChallenge{}, false, tx.Commit(ctx)
	}
	if err != nil {
		return MFAChallenge{}, false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM mfa_challenges WHERE token_hash=$1`, tokenHash); err != nil {
		return MFAChallenge{}, false, err
	}
	return challenge, true, tx.Commit(ctx)
}

// DeleteMFAChallengesForSubject clears in-flight steps after a factor change,
// so a revocation cannot be outrun by a step that is already open.
func (s *Store) DeleteMFAChallengesForSubject(ctx context.Context, scope string, subjectID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM mfa_challenges WHERE scope=$1 AND subject_id=$2`, scope, subjectID)
	return err
}
