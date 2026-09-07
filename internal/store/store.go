// Package store provides PostgreSQL persistence and transactional payment operations.
package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-payment-demo/internal/crypto"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Store wraps PostgreSQL operations used by the application.
type Store struct {
	pool *pgxpool.Pool

	// EmailResendCooldown gates how often a new confirmation code can be
	// requested for the same user and email.
	EmailResendCooldown time.Duration

	// dataKey is the AES-256-GCM key for application-level encryption at rest.
	// When empty the store runs in plaintext passthrough (dev without the env
	// var); production requires DATA_ENCRYPTION_KEY in config.
	dataKey []byte
}

// SetDataKey configures the encryption-at-rest key. It must be called before
// any payload/session reads or writes.
func (s *Store) SetDataKey(key []byte) {
	s.dataKey = append([]byte(nil), key...)
}

// sealValue encrypts plaintext at rest, or returns it unchanged in
// no-key passthrough mode.
func (s *Store) sealValue(plain []byte) (string, error) {
	if len(s.dataKey) == 0 {
		return string(plain), nil
	}
	return crypto.Seal(s.dataKey, plain)
}

// openValue decrypts a stored value. Legacy plaintext rows pass through;
// encrypted envelopes require the configured key.
func (s *Store) openValue(value string) ([]byte, error) {
	return crypto.Open(s.dataKey, value)
}

// EncryptLegacyAtRest rewrites any remaining plaintext chat payload rows to
// sealed envelopes (batched, keyset-paginated). It is safe to run repeatedly.
func (s *Store) EncryptLegacyAtRest(ctx context.Context) (int, error) {
	if len(s.dataKey) == 0 {
		return 0, nil
	}
	encrypted := 0
	seal := func(key any, table, keyCol, payload string) error {
		sealed, err := crypto.Seal(s.dataKey, []byte(payload))
		if err != nil {
			return err
		}
		_, err = s.pool.Exec(ctx, fmt.Sprintf(`UPDATE %s SET payload=$2 WHERE %s=$1`, table, keyCol), key, sealed)
		return err
	}

	var textCursor string
	for {
		rows, err := s.pool.Query(ctx, `
			SELECT provider_message_id, payload FROM inbound_messages
			WHERE payload NOT LIKE 'enc:v1:%' AND provider_message_id > $1
			ORDER BY provider_message_id LIMIT 500`, textCursor)
		if err != nil {
			return encrypted, err
		}
		var keys []string
		var payloads []string
		for rows.Next() {
			var key, payload string
			if err := rows.Scan(&key, &payload); err != nil {
				rows.Close()
				return encrypted, err
			}
			keys = append(keys, key)
			payloads = append(payloads, payload)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return encrypted, err
		}
		if len(keys) == 0 {
			break
		}
		for i := range keys {
			if err := seal(keys[i], "inbound_messages", "provider_message_id", payloads[i]); err != nil {
				return encrypted, err
			}
			encrypted++
		}
		textCursor = keys[len(keys)-1]
	}

	var idCursor int64
	for {
		rows, err := s.pool.Query(ctx, `
			SELECT id, payload FROM message_outbox
			WHERE payload NOT LIKE 'enc:v1:%' AND id > $1
			ORDER BY id LIMIT 500`, idCursor)
		if err != nil {
			return encrypted, err
		}
		var keys []int64
		var payloads []string
		for rows.Next() {
			var key int64
			var payload string
			if err := rows.Scan(&key, &payload); err != nil {
				rows.Close()
				return encrypted, err
			}
			keys = append(keys, key)
			payloads = append(payloads, payload)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return encrypted, err
		}
		if len(keys) == 0 {
			break
		}
		for i := range keys {
			if err := seal(keys[i], "message_outbox", "id", payloads[i]); err != nil {
				return encrypted, err
			}
			encrypted++
		}
		idCursor = keys[len(keys)-1]
	}
	return encrypted, nil
}

// Admin role names for the RBAC admin console.
const (
	RoleAdmin      = "admin"
	RoleCompliance = "compliance"
	RoleSupport    = "support"
	RoleReadOnly   = "readonly"
)

// AdminUser is a named operator account on the admin console.
type AdminUser struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	Role         string
	Enabled      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ChannelAPI is the channel recorded on payments initiated through the merchant
// Partner API. Their terminal transitions carry no customer message: there is
// no chat session to send one to, so transitionPayment skips the outbox row.
const ChannelAPI = "api"

// metadataValue returns a JSONB-safe payload for the payments.metadata column,
// defaulting to an empty object so the NOT NULL constraint is always satisfied.
func metadataValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

// Open establishes a PostgreSQL connection pool with tuned settings.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	cfg.MaxConns = 25
	cfg.MinConns = 5
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases database connections.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping checks database readiness.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Migrate applies embedded SQL migrations in filename order.
// migrationLockKey is a fixed advisory-lock key that serializes concurrent
// migration runners (multiple replicas / overlapping `migrate` invocations).
const migrationLockKey int64 = 0x7865676f5f6d6967

// Migrate applies all pending embedded SQL migrations. A session-level
// advisory lock is held for the whole run so two processes can never apply
// migrations concurrently, even across multiple app instances (C24).
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockKey) }()
	return migrateOn(ctx, conn)
}

func migrateOn(ctx context.Context, conn *pgxpool.Conn) error {
	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create migration registry: %w", err)
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var applied bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, entry.Name()).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if applied {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES($1)`, entry.Name()); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// Seed refreshes the baseline merchant fixtures plus the demo-visible
// merchant-adjacent rows that later migrations introduce: the business KYB
// baseline (B0, screened clear, approved) and the two system merchants used
// by the individual-pay and wallet-topup rails. Tests truncate these tables
// and call Seed, so Seed must reproduce the post-migrate state or KYB
// advancement and system-merchant lookups break on a fresh database.
func (s *Store) Seed(ctx context.Context) error {
	body, err := migrationFiles.ReadFile("migrations/002_seed_merchants.sql")
	if err != nil {
		return err
	}
	if _, err = s.pool.Exec(ctx, string(body)); err != nil {
		return err
	}
	// System merchants created by migrations 057/059. On CONFLICT we refresh
	// the metadata exactly like those migrations' upserts.
	if _, err = s.pool.Exec(ctx, `
		INSERT INTO merchants(slug,name,category,description,active,search_keywords,sort_order)
		VALUES
		    ('xego-individual-pay', 'Xego Individual Pay', 'Transfers',
		     'System recipient for Xego individual-to-individual transfer payments.',
		     false, 'xego individual pay transfer send money', 9999),
		    ('xego-wallet-topup', 'Xego Wallet Top-up', 'Wallet',
		     'System recipient for Xego wallet funding payments.',
		     false, 'xego wallet top up fund add money deposit', 9999)
		ON CONFLICT(slug) DO UPDATE
		SET name=EXCLUDED.name,
		    category=EXCLUDED.category,
		    description=EXCLUDED.description,
		    active=EXCLUDED.active,
		    search_keywords=EXCLUDED.search_keywords,
		    sort_order=EXCLUDED.sort_order`); err != nil {
		return err
	}
	// Grandfather every merchant into the B0 baseline with a clear screening
	// decision, mirroring migration 053, so truncate+seed leaves KYB
	// advancement unlocked exactly like a fresh migrate.
	if _, err = s.pool.Exec(ctx, `
		INSERT INTO business_kyb_profiles (merchant_id, tier, evidence, last_screening_decision, last_screen_at, rescreen_due, review_status)
		SELECT m.id, 'B0', '["registration_approved"]'::jsonb, 'clear', now(), now() + interval '90 days', 'approved'
		FROM merchants m
		ON CONFLICT (merchant_id) DO NOTHING`); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO users (whatsapp_number, display_name, email, account_level, onboarding_complete, number_confirmed_at)
		VALUES ('+2348000000001', 'Demo Merchant Admin', 'admin@xego.local', 'merchant', true, now())
		ON CONFLICT (whatsapp_number) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			account_level = 'merchant',
			onboarding_complete = true`)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO merchant_owners(merchant_id, user_id)
		SELECT m.id, u.id
		FROM merchants m
		JOIN users u ON u.whatsapp_number = '+2348000000001'
		ON CONFLICT DO NOTHING`)
	return err
}

func normalizePageBounds(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 25 {
		limit = 10
	}
	return offset, limit
}
