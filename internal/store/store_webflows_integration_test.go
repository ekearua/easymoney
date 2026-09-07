package store

import (
	"context"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestPostgresWebFlowPayloadAtRest verifies web-flow payloads (which now carry
// KYC profile text and OCR'd identity data) are sealed with the same crypto
// envelope as chat payloads, that the payment_id a flow references is promoted
// to its own indexable column for gateway lookups, and that EncryptLegacyAtRest
// re-seals any legacy plaintext rows.
func TestPostgresWebFlowPayloadAtRest(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	repository, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `TRUNCATE web_flows, users CASCADE`); err != nil {
		t.Fatal(err)
	}
	key, err := hex.DecodeString("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	repository.SetDataKey(key)

	user, err := repository.GetOrCreateUser(ctx, "+2348097777001")
	if err != nil {
		t.Fatal(err)
	}

	// Mint with the OCR'd identity text in the payload: the stored column must
	// be an envelope, never the plaintext JSON.
	flow, err := repository.MintWebFlow(ctx, user.ID, "whatsapp", "individual_upgrade",
		map[string]string{"id_slip": "NIN: 12345678901", "legal_name": "Ada Obi"}, "profile", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var sealed string
	if err := repository.pool.QueryRow(ctx, `SELECT payload FROM web_flows WHERE id=$1`, flow.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "enc:v1:") {
		t.Fatalf("web flow payload should be sealed at rest, got %q", sealed)
	}

	// Read-back decrypts to the original map.
	got, err := repository.WebFlowByToken(ctx, flow.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Payload["id_slip"] != "NIN: 12345678901" || got.Payload["legal_name"] != "Ada Obi" {
		t.Fatalf("decrypted web flow payload mismatch: %+v", got.Payload)
	}

	// SaveWebFlowProgress re-seals and promotes payment_id to the column.
	paymentID := uuid.New()
	if err := repository.SaveWebFlowProgress(ctx, flow.Token, "checkout", map[string]string{
		"payment_id": paymentID.String(),
		"merchant":   "lagos-lunchbox",
		"amount":     "250000",
	}); err != nil {
		t.Fatal(err)
	}
	var colPayment string
	if err := repository.pool.QueryRow(ctx, `SELECT payment_id FROM web_flows WHERE id=$1`, flow.ID).Scan(&colPayment); err != nil {
		t.Fatal(err)
	}
	if colPayment != paymentID.String() {
		t.Fatalf("payment_id column = %q, want %q", colPayment, paymentID.String())
	}
	var resealed string
	if err := repository.pool.QueryRow(ctx, `SELECT payload FROM web_flows WHERE id=$1`, flow.ID).Scan(&resealed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resealed, "enc:v1:") {
		t.Fatalf("saved web flow payload should stay sealed, got %q", resealed)
	}

	// The gateway lookup finds the open flow by the promoted column.
	byPayment, err := repository.OpenWebFlowByPayment(ctx, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	if byPayment.ID != flow.ID || byPayment.Payload["merchant"] != "lagos-lunchbox" {
		t.Fatalf("OpenWebFlowByPayment mismatch: %+v", byPayment)
	}
	if _, err := repository.OpenWebFlowByPayment(ctx, uuid.New()); err == nil {
		t.Fatal("unknown payment id must not resolve to a flow")
	}

	// Completing the flow still returns the decrypted payload.
	completed, claimed, err := repository.CompleteWebFlow(ctx, flow.Token)
	if err != nil || !claimed {
		t.Fatalf("complete flow: claimed=%v err=%v", claimed, err)
	}
	if completed.Payload["amount"] != "250000" {
		t.Fatalf("complete flow payload mismatch: %+v", completed.Payload)
	}

	// Legacy plaintext rows (pre-key writes) are re-sealed in place.
	legacyToken := "legacy-flow-token-" + time.Now().UTC().Format("150405.000000000")
	if _, err := repository.pool.Exec(ctx, `INSERT INTO web_flows (id, token, user_id, channel, flow_type, payload, step, status, expires_at)
		VALUES ($1,$2,$3,'whatsapp','individual_upgrade','{"id_slip":"NIN: 09876543210"}','profile','open',now()+interval '1 hour')`,
		uuid.New(), legacyToken, user.ID); err != nil {
		t.Fatal(err)
	}
	count, err := repository.EncryptLegacyAtRest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count < 1 {
		t.Fatalf("expected at least one legacy web flow row re-encrypted, got %d", count)
	}
	var legacySealed string
	if err := repository.pool.QueryRow(ctx, `SELECT payload FROM web_flows WHERE token=$1`, legacyToken).Scan(&legacySealed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(legacySealed, "enc:v1:") {
		t.Fatalf("legacy web flow payload should be re-sealed, got %q", legacySealed)
	}
}
