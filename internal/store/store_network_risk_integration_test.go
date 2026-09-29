package store

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/kyc"
)

// countRiskEvents counts one user's observations of a type, optionally with a
// details key equal to a value.
func countRiskEvents(t *testing.T, repository *Store, userID uuid.UUID, eventType, keyField, keyValue string) (int, float64) {
	t.Helper()
	query := `SELECT count(*), COALESCE(sum(score),0) FROM risk_events WHERE user_id=$1 AND event_type=$2`
	args := []any{userID, eventType}
	if keyField != "" {
		query += ` AND details->>$3=$4`
		args = append(args, keyField, keyValue)
	}
	var count int
	var total float64
	if err := repository.pool.QueryRow(context.Background(), query, args...).Scan(&count, &total); err != nil {
		t.Fatal(err)
	}
	return count, total
}

// TestSharedDeviceSignalMovesBothIdentities pins the device half of the Trust
// Fabric feed-back: the signal is recorded symmetrically (the identity that
// signed in second is not the only one carrying the link, and the earlier one
// gets it backfilled), once per identity, with no other customer's identifiers
// in the details a customer can export, and it moves the band.
func TestSharedDeviceSignalMovesBothIdentities(t *testing.T) {
	ctx := context.Background()
	repository := openRiskTestStore(t)
	first, err := repository.GetOrCreateUser(ctx, "+2348090002010")
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.GetOrCreateUser(ctx, "+2348090002011")
	if err != nil {
		t.Fatal(err)
	}
	const fingerprint = "fp-shared-browser-001"

	before, err := repository.RecomputeRiskScore(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	// One browser, one identity: nothing to link yet.
	if err := repository.TouchTrustedDevice(ctx, first.ID, fingerprint, "Windows Chrome"); err != nil {
		t.Fatal(err)
	}
	if count, _ := countRiskEvents(t, repository, first.ID, kyc.RiskEventSharedDevice, "", ""); count != 0 {
		t.Fatalf("a device used by one identity is not a link, found %d observations", count)
	}

	// The same browser, a second identity: both sides get the observation.
	if err := repository.TouchTrustedDevice(ctx, second.ID, fingerprint, "Windows Chrome"); err != nil {
		t.Fatal(err)
	}
	for _, user := range []struct {
		id   uuid.UUID
		name string
	}{{first.ID, "first"}, {second.ID, "second"}} {
		count, total := countRiskEvents(t, repository, user.id, kyc.RiskEventSharedDevice, "fingerprint", fingerprint)
		if count != 1 {
			t.Fatalf("%s identity: expected exactly one shared-device observation, got %d", user.name, count)
		}
		if want := kyc.SharedDeviceRisk(1); total != want {
			t.Fatalf("%s identity: weight = %v, want %v", user.name, total, want)
		}
	}

	// No other customer's identifiers belong in an exportable record: the
	// details name the crowd, not the people in it.
	var leaked int
	if err := repository.pool.QueryRow(ctx, `
		SELECT count(*) FROM risk_events
		WHERE user_id=$1 AND event_type=$2
		  AND (details::text LIKE '%'||$3||'%' OR details::text LIKE '%'||$4||'%')`,
		first.ID, kyc.RiskEventSharedDevice, first.ID.String(), second.ID.String()).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("shared-device details must not carry any identity's identifier")
	}

	// A later sighting is not a new fact.
	if err := repository.TouchTrustedDevice(ctx, first.ID, fingerprint, "Windows Chrome"); err != nil {
		t.Fatal(err)
	}
	if count, _ := countRiskEvents(t, repository, first.ID, kyc.RiskEventSharedDevice, "", ""); count != 1 {
		t.Fatalf("re-touching the device must not restate the observation, found %d", count)
	}

	after, err := repository.RecomputeRiskScore(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RiskScore <= before.RiskScore {
		t.Fatalf("sharing a browser must move the score: before=%v after=%v", before.RiskScore, after.RiskScore)
	}
}

// TestRingMembershipSignalAndItsRelease pins the ring half: a ring case with
// confirmed evidence scores every member once (referenced by an opaque digest,
// not by the member list), the case itself is disposed of once for the whole
// ring, and only a false-positive disposition gives the weight back.
func TestRingMembershipSignalAndItsRelease(t *testing.T) {
	ctx := context.Background()
	repository := openRiskTestStore(t)
	alpha, err := repository.GetOrCreateUser(ctx, "+2348090002020")
	if err != nil {
		t.Fatal(err)
	}
	beta, err := repository.GetOrCreateUser(ctx, "+2348090002021")
	if err != nil {
		t.Fatal(err)
	}
	gamma, err := repository.GetOrCreateUser(ctx, "+2348090002022")
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := repository.RecomputeRiskScore(ctx, alpha.ID)
	if err != nil {
		t.Fatal(err)
	}

	pair := ScoredRing{
		Members:       []RingMember{{Subject: alpha.ID.String()}, {Subject: beta.ID.String()}},
		Kinds:         []string{"device"},
		Score:         21.5,
		ConfirmedHits: 1,
	}
	raised, err := repository.EnsureNetworkCases(ctx, []ScoredRing{pair}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if raised != 1 {
		t.Fatalf("expected one linked case for the ring, got %d", raised)
	}
	want := kyc.RingMembershipRisk(1, 2)
	for _, user := range []struct {
		id   uuid.UUID
		name string
	}{{alpha.ID, "alpha"}, {beta.ID, "beta"}} {
		count, total := countRiskEvents(t, repository, user.id, kyc.RiskEventRingMember, "", "")
		if count != 1 || total != want {
			t.Fatalf("%s: expected one observation of %v, got count=%d total=%v", user.name, want, count, total)
		}
	}
	// A ring scored with no confirmed evidence among its members is a lead,
	// not a finding, and scores nobody.
	if _, err := repository.EnsureNetworkCases(ctx, []ScoredRing{
		{
			Members: []RingMember{{Subject: beta.ID.String()}, {Subject: gamma.ID.String()}},
			Kinds:   []string{"channel"}, Score: 9, ConfirmedHits: 0,
		},
	}, 1); err != nil {
		t.Fatal(err)
	}
	if count, _ := countRiskEvents(t, repository, gamma.ID, kyc.RiskEventRingMember, "", ""); count != 0 {
		t.Fatalf("an unconfirmed ring must not score its members, found %d", count)
	}

	// The observation names the ring by digest, so a member's own record does
	// not list the identities it was linked to.
	var leaked int
	if err := repository.pool.QueryRow(ctx, `
		SELECT count(*) FROM risk_events
		WHERE user_id=$1 AND event_type=$2
		  AND (details::text LIKE '%'||$3||'%' OR details::text LIKE '%'||$4||'%')`,
		alpha.ID, kyc.RiskEventRingMember, alpha.ID.String(), beta.ID.String()).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("ring-membership details must not carry the member list")
	}

	// Re-running the pipeline is idempotent.
	if _, err := repository.EnsureNetworkCases(ctx, []ScoredRing{pair}, 1); err != nil {
		t.Fatal(err)
	}
	if count, _ := countRiskEvents(t, repository, alpha.ID, kyc.RiskEventRingMember, "", ""); count != 1 {
		t.Fatalf("re-running the case pipeline must not restate observations, found %d", count)
	}

	cases, err := repository.ListNetworkCases(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var pairCase NetworkCase
	for _, c := range cases {
		if c.MembersKey == alpha.ID.String()+","+beta.ID.String() {
			pairCase = c
		}
	}
	if pairCase.ID == 0 {
		t.Fatalf("expected a case covering the pair, got %+v", cases)
	}

	// A confirmed disposition keeps the weight.
	if _, err := repository.ResolveNetworkCase(ctx, pairCase.ID, "resolved", "rev@xego.test", "same operator, real business"); err != nil {
		t.Fatal(err)
	}
	if count, _ := countRiskEvents(t, repository, alpha.ID, kyc.RiskEventRingMember+"_cleared", "", ""); count != 0 {
		t.Fatalf("a confirmed case must not cancel membership, found %d cancellations", count)
	}

	// A false positive gives it back, exactly, once.
	if _, err := repository.ResolveNetworkCase(ctx, pairCase.ID, "false_positive", "rev@xego.test", "family laptop"); err != nil {
		t.Fatal(err)
	}
	for _, user := range []struct {
		id   uuid.UUID
		name string
	}{{alpha.ID, "alpha"}, {beta.ID, "beta"}} {
		count, total := countRiskEvents(t, repository, user.id, kyc.RiskEventRingMember+"_cleared", "", "")
		if count != 1 {
			t.Fatalf("%s: expected exactly one cancellation, got %d", user.name, count)
		}
		if total != -want {
			t.Fatalf("%s: cancellation = %v, want %v", user.name, total, -want)
		}
	}
	// Re-resolving writes no second cancellation.
	if _, err := repository.ResolveNetworkCase(ctx, pairCase.ID, "false_positive", "rev@xego.test", "family laptop"); err != nil {
		t.Fatal(err)
	}
	if count, _ := countRiskEvents(t, repository, alpha.ID, kyc.RiskEventRingMember+"_cleared", "", ""); count != 1 {
		t.Fatalf("re-resolution must be idempotent, found %d cancellations", count)
	}

	settled, err := repository.RecomputeRiskScore(ctx, alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.RiskScore != baseline.RiskScore {
		t.Fatalf("a cleared ring must leave the score where it was: want %v, got %v", baseline.RiskScore, settled.RiskScore)
	}

	// The cancellation is dated with the observation it corrects, so the pair
	// cancels under any decay half-life.
	var sameTime bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT o.occurred_at = c.occurred_at AND o.score = -c.score
		FROM risk_events o
		JOIN risk_events c ON c.user_id=o.user_id
		WHERE o.user_id=$1 AND o.event_type=$2 AND c.event_type=$3`,
		alpha.ID, kyc.RiskEventRingMember, kyc.RiskEventRingMember+"_cleared").Scan(&sameTime); err != nil {
		t.Fatal(err)
	}
	if !sameTime {
		t.Fatal("the cancellation must be dated with, and equal to, the observation it corrects")
	}

	// A digest, not the member list.
	var ref string
	if err := repository.pool.QueryRow(ctx, `
		SELECT COALESCE(details->>'ring_ref','') FROM risk_events
		WHERE user_id=$1 AND event_type=$2 LIMIT 1`,
		alpha.ID, kyc.RiskEventRingMember).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if ref == "" || strings.Contains(ref, alpha.ID.String()) {
		t.Fatalf("ring reference must be an opaque digest, got %q", ref)
	}
}
