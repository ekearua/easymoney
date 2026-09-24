package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// mfaStore returns a migrated store against TEST_DATABASE_URL, or skips.
func mfaStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	if testing.Short() {
		t.Skip("skipping DB-gated test in short mode")
	}
	ctx := context.Background()
	repository, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return repository, ctx
}

// TestMFAFactorLifecycle exercises the factor store the way the login flow and
// the security pages use it: one authenticator per subject, passkeys per
// device, revocation, and the signature-counter update a passkey assertion
// performs.
func TestMFAFactorLifecycle(t *testing.T) {
	repository, ctx := mfaStore(t)
	subject := uuid.New()
	t.Cleanup(func() {
		_, _ = repository.pool.Exec(context.Background(), `DELETE FROM mfa_factors WHERE subject_id=$1`, subject)
	})

	cipher := []byte{1, 2, 3, 4}
	id, err := repository.UpsertMFAFactor(ctx, MFAFactor{
		Scope: MFAScopeMerchant, SubjectID: subject, Kind: MFAKindTOTP, SecretCipher: cipher,
	})
	if err != nil {
		t.Fatal(err)
	}
	totpFactor, err := repository.MFAFactorByKind(ctx, MFAScopeMerchant, subject, MFAKindTOTP)
	if err != nil || totpFactor == nil {
		t.Fatalf("totp factor lookup: %v (factor %v)", err, totpFactor)
	}
	if totpFactor.ID != id || string(totpFactor.SecretCipher) != string(cipher) {
		t.Fatalf("totp factor = %+v, want id %d with the stored cipher", totpFactor, id)
	}
	if totpFactor.Label != "Authenticator app" {
		t.Fatalf("label = %q, want the default authenticator label", totpFactor.Label)
	}
	if totpFactor.ConfirmedAt != nil {
		t.Fatal("a fresh factor is unconfirmed until its first successful challenge")
	}

	// Re-enrolling the same kind replaces the secret instead of adding a row.
	replacement := []byte{9, 9}
	if _, err := repository.UpsertMFAFactor(ctx, MFAFactor{
		Scope: MFAScopeMerchant, SubjectID: subject, Kind: MFAKindTOTP, SecretCipher: replacement,
	}); err != nil {
		t.Fatal(err)
	}
	factors, err := repository.MFAFactors(ctx, MFAScopeMerchant, subject)
	if err != nil {
		t.Fatal(err)
	}
	if len(factors) != 1 || string(factors[0].SecretCipher) != string(replacement) {
		t.Fatalf("re-enrollment must replace the single authenticator, got %+v", factors)
	}

	// Email is a second, separate factor of the same subject.
	emailID, err := repository.UpsertMFAFactor(ctx, MFAFactor{
		Scope: MFAScopeMerchant, SubjectID: subject, Kind: MFAKindEmail, Label: "Email code",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmMFAFactor(ctx, emailID); err != nil {
		t.Fatal(err)
	}
	if err := repository.TouchMFAFactor(ctx, emailID); err != nil {
		t.Fatal(err)
	}

	// Passkeys are per-device, so the subject may hold several at once.
	credential := []byte("credential-one")
	passkeyID, err := repository.InsertPasskeyFactor(ctx, MFAFactor{
		Scope: MFAScopeMerchant, SubjectID: subject, Kind: MFAKindPasskey,
		Label: "Pixel 9", CredentialID: credential, PublicKey: []byte("cose-key"),
		Transports: "internal,hybrid",
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := repository.MFAFactorByCredential(ctx, credential)
	if err != nil || resolved == nil || resolved.ID != passkeyID {
		t.Fatalf("credential lookup: %v (factor %v)", err, resolved)
	}
	if resolved.Label != "Pixel 9" || resolved.Transports != "internal,hybrid" {
		t.Fatalf("passkey = %+v, want the registered label and transports", resolved)
	}
	if err := repository.UpdatePasskeySignCount(ctx, passkeyID, 12); err != nil {
		t.Fatal(err)
	}
	resolved, err = repository.MFAFactorByCredential(ctx, credential)
	if err != nil || resolved == nil || resolved.SignCount != 12 {
		t.Fatalf("sign counter after update = %v (%v)", resolved, err)
	}

	count, err := repository.MFAFactorCount(ctx, MFAScopeMerchant, subject)
	if err != nil || count != 3 {
		t.Fatalf("factor count = %d (%v), want 3", count, err)
	}

	if err := repository.DeleteMFAFactor(ctx, MFAScopeMerchant, subject, passkeyID); err != nil {
		t.Fatal(err)
	}
	if resolved, err := repository.MFAFactorByCredential(ctx, credential); err != nil || resolved != nil {
		t.Fatalf("revoked credential still resolves: %v (%v)", resolved, err)
	}
	if err := repository.RenameMFAFactor(ctx, MFAScopeMerchant, subject, emailID, "Work email"); err != nil {
		t.Fatal(err)
	}
	emailFactor, err := repository.MFAFactorByKind(ctx, MFAScopeMerchant, subject, MFAKindEmail)
	if err != nil || emailFactor == nil || emailFactor.Label != "Work email" {
		t.Fatalf("rename: %v (%v)", err, emailFactor)
	}

	removed, err := repository.DeleteMFAFactorsForSubject(ctx, MFAScopeMerchant, subject)
	if err != nil || removed != 2 {
		t.Fatalf("subject reset removed %d factors (%v), want 2", removed, err)
	}
	if factors, err := repository.MFAFactors(ctx, MFAScopeMerchant, subject); err != nil || len(factors) != 0 {
		t.Fatalf("factors after reset = %v (%v)", factors, err)
	}
	// The reset is scoped: another subject keeps its factors.
	other := uuid.New()
	t.Cleanup(func() {
		_, _ = repository.pool.Exec(context.Background(), `DELETE FROM mfa_factors WHERE subject_id=$1`, other)
	})
	if _, err := repository.UpsertMFAFactor(ctx, MFAFactor{Scope: MFAScopeAdmin, SubjectID: other, Kind: MFAKindTOTP}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DeleteMFAFactorsForSubject(ctx, MFAScopeAdmin, subject); err != nil {
		t.Fatal(err)
	}
	if count, err := repository.MFAFactorCount(ctx, MFAScopeAdmin, other); err != nil || count != 1 {
		t.Fatalf("unrelated subject count = %d (%v), want 1", count, err)
	}
}

// TestMFAChallengeAttemptBudget pins the retry semantics the two-step page
// depends on: a wrong code leaves the step live with one attempt spent, and the
// step disappears only when the budget is exhausted.
func TestMFAChallengeAttemptBudget(t *testing.T) {
	repository, ctx := mfaStore(t)
	subject := uuid.New()
	tokenHash := []byte(fmt.Sprintf("challenge-%s", subject))
	t.Cleanup(func() {
		_, _ = repository.pool.Exec(context.Background(), `DELETE FROM mfa_challenges WHERE subject_id=$1`, subject)
	})
	if err := repository.CreateMFAChallenge(ctx, tokenHash, MFAChallenge{
		Scope: MFAScopeMerchant, SubjectID: subject, Method: MFAKindEmail,
		CodeHash: []byte("bcrypt-hash"), ExpiresAt: time.Now().Add(10 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	challenge, found, err := repository.MFAChallengeByToken(ctx, tokenHash)
	if err != nil || !found {
		t.Fatalf("live challenge lookup: %v (found %v)", err, found)
	}
	if challenge.Attempts != 0 || challenge.Method != MFAKindEmail {
		t.Fatalf("challenge = %+v, want a fresh email step", challenge)
	}

	const budget = 5
	for i := 1; i <= budget; i++ {
		remaining, err := repository.FailMFAChallenge(ctx, tokenHash, budget)
		if err != nil {
			t.Fatal(err)
		}
		if want := budget - i; remaining != want {
			t.Fatalf("attempt %d: remaining = %d, want %d", i, remaining, want)
		}
		_, found, err = repository.MFAChallengeByToken(ctx, tokenHash)
		if err != nil {
			t.Fatal(err)
		}
		if wantLive := i < budget; found != wantLive {
			t.Fatalf("attempt %d: step live = %v, want %v", i, found, wantLive)
		}
	}

	// A fresh step survives its first wrong code and is consumed only once the
	// code matches.
	if err := repository.CreateMFAChallenge(ctx, tokenHash, MFAChallenge{
		Scope: MFAScopeMerchant, SubjectID: subject, Method: MFAKindTOTP,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.FailMFAChallenge(ctx, tokenHash, budget); err != nil {
		t.Fatal(err)
	}
	consumed, found, err := repository.ConsumeMFAChallenge(ctx, tokenHash)
	if err != nil || !found {
		t.Fatalf("consume after one wrong code: %v (found %v)", err, found)
	}
	if consumed.Attempts != 1 {
		t.Fatalf("consumed attempts = %d, want the recorded failure", consumed.Attempts)
	}
	if _, found, err := repository.ConsumeMFAChallenge(ctx, tokenHash); err != nil || found {
		t.Fatalf("a consumed step must not be replayable: %v (found %v)", err, found)
	}
}

// TestMFABackfillMigratesLegacyTOTP runs the shipped migration's backfill
// statement against a legacy enrollment and asserts it lands as a factor, while
// an unattributable legacy row (NULL subject_id) is left behind. The statement
// is read from the embedded migration so the test cannot drift from what
// actually ships.
func TestMFABackfillMigratesLegacyTOTP(t *testing.T) {
	repository, ctx := mfaStore(t)
	subject := uuid.New()
	cipher := []byte{7, 7, 7}
	t.Cleanup(func() {
		_, _ = repository.pool.Exec(context.Background(), `DELETE FROM mfa_factors WHERE subject_id=$1`, subject)
		_, _ = repository.pool.Exec(context.Background(), `DELETE FROM totp_secrets WHERE scope='merchant' AND subject_id=$1`, subject)
	})

	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO totp_secrets(scope, subject_id, secret_cipher)
		VALUES('merchant', $1, $2)`, subject, cipher); err != nil {
		t.Fatal(err)
	}
	// A legacy row with no subject cannot be attributed to an account and must
	// not be backfilled.
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO totp_secrets(scope, subject_id, secret_cipher)
		VALUES('admin', NULL, $1)`, cipher); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repository.pool.Exec(context.Background(),
			`DELETE FROM totp_secrets WHERE scope='admin' AND subject_id IS NULL`)
	})

	statement := migrationBackfillStatement(t, "071_mfa_factors.sql")
	if _, err := repository.pool.Exec(ctx, statement); err != nil {
		t.Fatalf("backfill statement failed: %v", err)
	}

	factor, err := repository.MFAFactorByKind(ctx, MFAScopeMerchant, subject, MFAKindTOTP)
	if err != nil || factor == nil {
		t.Fatalf("backfilled factor missing: %v (%v)", err, factor)
	}
	if string(factor.SecretCipher) != string(cipher) {
		t.Fatal("the backfilled factor must carry the legacy cipher unchanged")
	}
	if factor.ConfirmedAt == nil {
		t.Fatal("a backfilled enrollment is already confirmed; the subject must not be asked to scan again")
	}
	var orphans int
	if err := repository.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM mfa_factors WHERE scope='admin' AND secret_cipher=$1`,
		cipher).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	// The unattributable legacy row must have produced nothing: subject_id is
	// NOT NULL in the new table, so there is nowhere for it to land.
	if orphans != 0 {
		t.Fatalf("unattributable legacy rows produced %d factors", orphans)
	}
}

// migrationBackfillStatement extracts the statement that follows the
// "-- Backfill" marker in an embedded migration file.
func migrationBackfillStatement(t *testing.T, name string) string {
	t.Helper()
	raw, err := migrationFiles.ReadFile("migrations/" + name)
	if err != nil {
		t.Fatalf("read embedded migration %s: %v", name, err)
	}
	text := string(raw)
	index := strings.Index(text, "-- Backfill")
	if index < 0 {
		t.Fatalf("migration %s has no backfill section", name)
	}
	statement := strings.Split(text[index:], ";")[0]
	statement = strings.TrimSpace(statement)
	if statement == "" {
		t.Fatalf("migration %s has an empty backfill section", name)
	}
	return statement
}
