package store

// TestIdentityLinksAndRings proves the network layer's SQL end to end:
// a shared device fingerprint and a shared settlement account produce
// edges (and rings via DetectRings), unrelated subjects produce none, and
// the per-subject trust timeline merges alerts, device sightings and
// user-scoped audit entries into one chronological feed.
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run "TestIdentityLinksAndRings|TestTrustTimelineMergesSources" -v

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
)

func TestIdentityLinksAndRings(t *testing.T) {
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE users, merchants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"trusted_devices", "merchant_owners", "merchant_settlement_accounts", "settlement_batches", "payouts"} {
		if _, err := repository.pool.Exec(ctx, `TRUNCATE `+tbl+` RESTART IDENTITY CASCADE`); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	ada, err := repository.GetOrCreateUser(ctx, "+2348095550201")
	if err != nil {
		t.Fatal(err)
	}
	bode, err := repository.GetOrCreateUser(ctx, "+2348095550202")
	if err != nil {
		t.Fatal(err)
	}
	related, err := repository.GetOrCreateUser(ctx, "+2348095550203")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := repository.GetOrCreateUser(ctx, "+2348095550204")
	if err != nil {
		t.Fatal(err)
	}

	// Display names are empty by default; set them so the edge query's
	// name-resolution path is exercised.
	for _, u := range []struct {
		id   uuid.UUID
		name string
	}{{ada.ID, "Ada Ring"}, {bode.ID, "Bode Ring"}, {related.ID, "Related Ring"}, {stranger.ID, "Stranger"}} {
		if _, err := repository.pool.Exec(ctx, `UPDATE users SET display_name=$2 WHERE id=$1`, u.id, u.name); err != nil {
			t.Fatal(err)
		}
	}

	// Device edge: Ada and Bode share one browser fingerprint.
	sharedFP := DeviceFingerprint("Mozilla/5.0 Shared Browser", "en-NG")
	for _, u := range []uuid.UUID{ada.ID, bode.ID} {
		if err := repository.TouchTrustedDevice(ctx, u, sharedFP, "Shared Chrome"); err != nil {
			t.Fatal(err)
		}
	}
	// The stranger's device is its own.
	if err := repository.TouchTrustedDevice(ctx, stranger.ID, DeviceFingerprint("Mozilla/5.0 Lone Browser", "en-NG"), "Lone Safari"); err != nil {
		t.Fatal(err)
	}

	// Settlement edge: two merchants owned by Bode and Related share one
	// bank account (the classic mule-merchant pattern).
	bank, err := createTestMerchant(ctx, repository, "ring-mule-bank", "Ring Mule Bank")
	if err != nil {
		t.Fatal(err)
	}
	shop, err := createTestMerchant(ctx, repository, "ring-mule-shop", "Ring Mule Shop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `INSERT INTO merchant_owners (merchant_id, user_id) VALUES ($1,$2), ($3,$4)`,
		bank.ID, bode.ID, shop.ID, related.ID); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct{ id uuid.UUID }{{bank.ID}, {shop.ID}} {
		if _, err := repository.pool.Exec(ctx, `
			INSERT INTO merchant_settlement_accounts (merchant_id, bank_code, account_number, account_name, is_default)
			VALUES ($1, '058', '0123456789', 'Mule Account', true)
			ON CONFLICT (merchant_id, bank_code, account_number) DO NOTHING`, m.id); err != nil {
			t.Fatal(err)
		}
	}

	links, err := repository.IdentityLinks(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var deviceEdge, settlementEdge bool
	for _, l := range links {
		if l.LinkKind == "device" && l.LinkValue == sharedFP {
			deviceEdge = true
			if !((l.SubjectA == ada.ID.String() && l.SubjectB == bode.ID.String()) ||
				(l.SubjectA == bode.ID.String() && l.SubjectB == ada.ID.String())) {
				t.Errorf("device edge pairs the wrong subjects: %+v", l)
			}
			if l.NameA == "" && l.NameB == "" {
				t.Errorf("device edge resolved no display names: %+v", l)
			}
		}
		if l.LinkKind == "settlement_account" {
			settlementEdge = true
		}
	}
	if !deviceEdge {
		t.Errorf("no device edge for the shared fingerprint; links=%+v", links)
	}
	if !settlementEdge {
		t.Errorf("no settlement-account edge for the shared account; links=%+v", links)
	}

	// The stranger appears in no edge.
	for _, l := range links {
		if l.SubjectA == stranger.ID.String() || l.SubjectB == stranger.ID.String() {
			t.Errorf("stranger must not be linked: %+v", l)
		}
	}

	// Ada + Bode + Related form one transitively-connected ring.
	rings := DetectRings(links, 2)
	if len(rings) != 1 {
		t.Fatalf("got %d rings, want 1 transitive ring; %+v", len(rings), rings)
	}
	if len(rings[0].Members) != 3 {
		t.Errorf("ring members = %v, want the three linked subjects", rings[0].Members)
	}
}

func TestTrustTimelineMergesSources(t *testing.T) {
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
	if _, err := repository.pool.Exec(ctx, `TRUNCATE users, merchants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"transaction_alerts", "trusted_devices", "audit_logs"} {
		if _, err := repository.pool.Exec(ctx, `TRUNCATE `+tbl+` RESTART IDENTITY CASCADE`); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	payer, err := repository.GetOrCreateUser(ctx, "+2348095550211")
	if err != nil {
		t.Fatal(err)
	}

	// One of each source: alert, device, audit.
	if _, err := repository.recordMonitorAlerts(ctx, payer.ID, []kyc.MonitorAlert{{
		Rule: "velocity", Severity: "high", Rails: []string{"payment"}, References: []string{"TL-REF-1"},
	}}); err != nil {
		t.Fatalf("seed alert: %v", err)
	}
	if err := repository.TouchTrustedDevice(ctx, payer.ID, DeviceFingerprint("Mozilla/5.0 Timeline Browser", "en-NG"), "Timeline Chrome"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AppendAuditLog(ctx, AuditLog{
		ActorType:    "system",
		Action:       "kyc.tier_advanced",
		ResourceType: sqlStr("user"),
		ResourceID:   sqlStr(payer.ID.String()),
		Details:      map[string]any{},
	}); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	entries, err := repository.TrustTimeline(ctx, payer.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, e := range entries {
		kinds[e.Kind]++
	}
	// Alert ingestion also records a user-scoped risk audit entry, so the
	// audit count is >= 1; alert and device are exactly one each.
	if kinds["alert"] != 1 || kinds["device"] != 1 || kinds["audit"] < 1 {
		t.Fatalf("timeline kinds = %v, want 1 alert, 1 device, >=1 audit", kinds)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].When.Before(entries[i].When) {
			t.Errorf("timeline not newest-first at %d: %v before %v", i, entries[i-1].When, entries[i].When)
		}
	}
	// Another subject sees nothing of it.
	other, err := repository.GetOrCreateUser(ctx, "+2348095550212")
	if err != nil {
		t.Fatal(err)
	}
	if entries2, err := repository.TrustTimeline(ctx, other.ID, 50); err != nil || len(entries2) != 0 {
		t.Fatalf("other subject timeline = %v (err %v), want empty", entries2, err)
	}
}

// createTestMerchant inserts a minimal active merchant row directly (the
// seed file's shape) and returns it.
func createTestMerchant(ctx context.Context, s *Store, slug, name string) (Merchant, error) {
	var m Merchant
	err := s.pool.QueryRow(ctx, `
		INSERT INTO merchants (slug, name, category, description, active)
		VALUES ($1, $2, 'other', '', true)
		RETURNING id, slug, name`, slug, name).Scan(&m.ID, &m.Slug, &m.Name)
	if err != nil {
		return Merchant{}, fmt.Errorf("create test merchant: %w", err)
	}
	return m, nil
}
