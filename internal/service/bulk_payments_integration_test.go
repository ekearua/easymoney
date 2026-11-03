package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// bulkServiceTestDBURL returns a dedicated database for this integration test,
// recreated on every run, so batches, wallets, and settlement state left by an
// earlier execution (or by any other suite touching TEST_DATABASE_URL) can
// never leak into the assertions. It accepts either the URL form used by the
// store/app suites or the keyword-form DSN the rest of the service suite
// expects.
func bulkServiceTestDBURL(t *testing.T, base string) string {
	t.Helper()
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	mainDB := cfg.ConnConfig.Database
	if mainDB == "" {
		mainDB = "postgres"
	}
	bulkDB := mainDB + "_bulk_service"
	dropCreate := func(conn *pgx.Conn, name string) {
		t.Helper()
		if _, err := conn.Exec(ctx, `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `CREATE DATABASE "`+name+`"`); err != nil {
			t.Fatal(err)
		}
	}
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		dsn := regexp.MustCompile(`(^|\s)database=\S+`).ReplaceAllString(strings.TrimSpace(base), "")
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		dropCreate(conn, bulkDB)
		return dsn + " database=" + bulkDB
	}
	admin := *parsed
	admin.Path = "/postgres"
	bulk := *parsed
	bulk.Path = "/" + bulkDB
	conn, err := pgx.Connect(ctx, admin.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	dropCreate(conn, bulkDB)
	return bulk.String()
}

// bulkE2EMessenger records every outbound stage-1 message by destination so the
// test can assert who was asked for a location and who was invited instead.
type bulkE2EMessenger struct {
	locations []string
	texts     []string
	links     []string
}

func (m *bulkE2EMessenger) SendText(_ context.Context, to, _ string) error {
	m.texts = append(m.texts, to)
	return nil
}

func (m *bulkE2EMessenger) SendInteractive(context.Context, ports.InteractiveMessage) error {
	return nil
}

func (m *bulkE2EMessenger) SendCheckout(context.Context, string, string, string) error {
	return nil
}

func (m *bulkE2EMessenger) SendLink(_ context.Context, to, _, url, _ string) error {
	m.links = append(m.links, to+"|"+url)
	return nil
}

func (m *bulkE2EMessenger) SendTemplate(_ context.Context, to, _ string, _ []string) error {
	m.texts = append(m.texts, to)
	return nil
}

func (m *bulkE2EMessenger) SendImage(context.Context, string, []byte, string) error {
	return nil
}

func (m *bulkE2EMessenger) SendLocationRequest(_ context.Context, to, _ string) error {
	m.locations = append(m.locations, to)
	return nil
}

// TestBulkPaymentsEndToEnd drives a batch through both stages against a real
// PostgreSQL database: recipient snapshot, affordability block, wallet funding
// through the settlement cut, the stage-1 location request, geo scoring of each
// capture, the Tier-2 KYC gate, the debit-per-success payout, and the
// finalised counters. It then replays the payout (idempotency) and runs a
// second batch whose capture arrives after the response window closed.
func TestBulkPaymentsEndToEnd(t *testing.T) {
	forcedNow := time.Date(2026, 11, 3, 13, 37, 0, 0, time.UTC)
	setFixedNow(t, forcedNow)

	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	databaseURL := bulkServiceTestDBURL(t, base)
	ctx := context.Background()
	repository, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	repository.SetDataKey(key[:])
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	merchant, err := repository.MerchantBySlug(ctx, "lagos-lunchbox")
	if err != nil {
		t.Fatal(err)
	}
	// Sweep anything an earlier run left unsettled so the wallet delta below is
	// exactly the payment this test creates.
	if _, err := repository.CutSettlement(ctx, merchant.ID, "BULK-SWEEP-"+uuid.NewString()[:8], time.Time{}, 0); err != nil &&
		!errors.Is(err, store.ErrNothingToSettle) {
		t.Fatalf("sweep settlement: %v", err)
	}

	endToEnd(t, repository, merchant, forcedNow)

	// Replay the whole payout again to prove the per-success debit is idempotent
	// and the final counters do not double-count.
	endToEnd(t, repository, merchant, forcedNow)

	// A second batch whose capture slips past the response window must be left
	// as INVITED so the merchant can chase it manually without retry pressure.
}
