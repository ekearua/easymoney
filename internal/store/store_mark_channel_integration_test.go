package store

import (
	"context"
	"os"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/kyc"
)

// TestPostgresMarkChannelOnboardedIsKYCNeutral verifies the durable channel
// stamp used by the cross-channel identity carry-over for already-approved
// customers. It must set ONLY the channel's confirmed timestamp (idempotently
// via COALESCE), and must never downgrade verification_level, account_level, or
// the KYC tier - unlike the Confirm*Account family which also demotes the
// verification level to the channel-specific value.
func TestPostgresMarkChannelOnboardedIsKYCNeutral(t *testing.T) {
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
	if _, err := repository.pool.Exec(ctx, `
		TRUNCATE message_outbox,inbound_messages,payment_events,payments,
		         conversation_sessions,users,merchants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}

	// An approved individual: confirmed WhatsApp number, individual account
	// label, clean screening, L2 ladder -> the profile a money-out rail trusts.
	user, err := repository.GetOrCreateUser(ctx, "+2348012340001")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmUserNumber(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertIndividualProfile(ctx, user.ID, "Ada Obi",
		time.Date(1992, 5, 24, 0, 0, 0, 0, time.UTC), "14 Admiralty Way, Lekki, Lagos", "Product designer"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordScreeningResult(ctx, ScreeningResult{
		UserID: user.ID, Provider: "simulated", Decision: kyc.ScreenClear,
	}, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceKYCTierTo(ctx, user.ID, kyc.TierL2,
		[]string{kyc.EvChannelConfirmed, kyc.EvIdentityOnFile}, nil); err != nil {
		t.Fatal(err)
	}

	before, err := repository.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	// First stamp: telegram channel becomes onboarded.
	if err := repository.MarkChannelOnboarded(ctx, user.ID, "telegram"); err != nil {
		t.Fatal(err)
	}
	stamped, err := repository.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stamped.TelegramConfirmedAt.Valid {
		t.Fatal("telegram_confirmed_at should be set after MarkChannelOnboarded")
	}

	// Idempotency: a second stamp must not back-date the timestamp.
	time.Sleep(1100 * time.Millisecond)
	if err := repository.MarkChannelOnboarded(ctx, user.ID, "telegram"); err != nil {
		t.Fatal(err)
	}
	restamped, err := repository.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !restamped.TelegramConfirmedAt.Valid || !restamped.TelegramConfirmedAt.Time.Equal(stamped.TelegramConfirmedAt.Time) {
		t.Fatalf("MarkChannelOnboarded must be idempotent: before %v after %v",
			stamped.TelegramConfirmedAt.Time, restamped.TelegramConfirmedAt.Time)
	}

	// KYC-neutrality: nothing that gates money-out may change.
	if restamped.VerificationLevel != before.VerificationLevel {
		t.Fatalf("verification_level changed %q -> %q", before.VerificationLevel, restamped.VerificationLevel)
	}
	if restamped.AccountLevel != before.AccountLevel {
		t.Fatalf("account_level changed %q -> %q", before.AccountLevel, restamped.AccountLevel)
	}
	profile, err := repository.KYCProfileByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kyc.Order(profile.Tier) < kyc.Order(kyc.TierL2) {
		t.Fatalf("KYC tier downgraded: %s", profile.Tier)
	}
	// The other channels are untouched.
	if restamped.InstagramConfirmedAt.Valid || restamped.TikTokConfirmedAt.Valid {
		t.Fatal("MarkChannelOnboarded(telegram) must not touch other channels")
	}

	// Unknown channel is a no-op (nil error, no mutation).
	if err := repository.MarkChannelOnboarded(ctx, user.ID, "carrier-pigeon"); err != nil {
		t.Fatalf("unknown channel should be a no-op, got %v", err)
	}
}

// TestPostgresMarkChannelOnboardedCoversAllChannels verifies each supported
// channel column gets stamped and never downgrades the verification level.
func TestPostgresMarkChannelOnboardedCoversAllChannels(t *testing.T) {
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
	if _, err := repository.pool.Exec(ctx, `
		TRUNCATE message_outbox,inbound_messages,payment_events,payments,
		         conversation_sessions,users,merchants RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		channel string
		check   func(User) bool
	}{
		{"whatsapp", func(u User) bool { return u.NumberConfirmedAt.Valid }},
		{"telegram", func(u User) bool { return u.TelegramConfirmedAt.Valid }},
		{"instagram", func(u User) bool { return u.InstagramConfirmedAt.Valid }},
		{"tiktok", func(u User) bool { return u.TikTokConfirmedAt.Valid }},
	}

	for _, tc := range cases {
		user, err := repository.GetOrCreateUser(ctx, "+2348012340002")
		if err != nil {
			t.Fatal(err)
		}
		level, err := repository.UserByID(ctx, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.MarkChannelOnboarded(ctx, user.ID, tc.channel); err != nil {
			t.Fatalf("%s: %v", tc.channel, err)
		}
		after, err := repository.UserByID(ctx, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !tc.check(after) {
			t.Fatalf("%s: confirmed stamp not set", tc.channel)
		}
		if after.VerificationLevel != level.VerificationLevel {
			t.Fatalf("%s: verification_level changed %q -> %q", tc.channel, level.VerificationLevel, after.VerificationLevel)
		}
	}
}
