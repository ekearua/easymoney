package app

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// bulkTestDBURL creates a dedicated database for the bulk-payment tests,
// recreated on every run, so batches and wallet balances left by an earlier
// execution of any suite touching TEST_DATABASE_URL can never leak into the
// assertions.
func bulkTestDBURL(t *testing.T, base string) string {
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
	bulkDB := mainDB + "_bulk_app"
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		t.Fatalf("TEST_DATABASE_URL must be a postgres URL, got %q", base)
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
	if _, err := conn.Exec(ctx, `DROP DATABASE IF EXISTS "`+bulkDB+`" WITH (FORCE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `CREATE DATABASE "`+bulkDB+`"`); err != nil {
		t.Fatal(err)
	}
	return bulk.String()
}
