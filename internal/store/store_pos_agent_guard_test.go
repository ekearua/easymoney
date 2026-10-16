package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoRemittancePlaintextSecrets is the negative guard behind
// POS_AGENT_NETWORK_PLAN.md §8: a pickup code, an OTP and a recipient's
// government ID must never be persisted in plaintext. Each is stored as a
// salted one-way digest (code_hash/code_salt, otp_hash/otp_salt), a sealed
// envelope (receiver_id_number_enc) or a deterministic cap-key hash
// (receiver_id_hash).
//
// The cheapest way to lose that property is a migration column added "just to
// show the code in an admin view". This guard reads the agent-network migration
// and fails on a bare secret-shaped column inside a remittance table; the
// suffixed, safe forms are allowed. Non-remittance tables (an agent region's
// public `code`, for instance) are out of scope.
func TestNoRemittancePlaintextSecrets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "store", "migrations", "103_pos_agent_network.sql")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read agent-network migration: %v", err)
	}

	table := ""
	for i, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		if m := createTableStmt.FindStringSubmatch(trimmed); len(m) >= 2 {
			table = strings.ToLower(m[1])
			continue
		}
		if trimmed == ");" || trimmed == ")" {
			table = ""
			continue
		}
		if !remittanceSecretTable(table) {
			continue
		}
		normalized := sqlAddColumn.ReplaceAllString(trimmed, "")
		if m := sqlColumnDef.FindStringSubmatch(normalized); len(m) >= 2 {
			if remittanceSecretColumn.MatchString(m[1]) {
				t.Errorf("103_pos_agent_network.sql:%d: plaintext secret column %q in %s — store only a hash, salt or sealed envelope", i+1, m[1], table)
			}
		}
	}
}

// createTableStmt captures the table name of a CREATE TABLE statement.
var createTableStmt = regexp.MustCompile(`(?i)^CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)`)

// remittanceSecretTable reports whether a table stores remittance secrets.
func remittanceSecretTable(table string) bool {
	switch table {
	case "remittances", "remittance_pickups", "remittance_otps", "remittance_payouts":
		return true
	}
	return false
}

// remittanceSecretColumn matches a column named for a remittance secret in its
// bare, plaintext form. Suffixed safe forms (code_hash, code_salt, otp_hash,
// otp_salt, receiver_id_number_enc, receiver_id_hash) are excluded by anchor.
var remittanceSecretColumn = regexp.MustCompile(`(?i)^(code|otp|pickup_?code|otp_?code|id_?number|receiver_?id_?number|pin)$`)
