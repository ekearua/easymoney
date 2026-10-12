package store

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoCardholderDataShapes is the negative guard behind
// SAVED_CARD_METHODS_PLAN.md §6 and §8.
//
// The platform holds no card data at all today: the card rail renders
// Interswitch's Hosted Fields iframes, so the PAN lives in Interswitch's frame
// and the platform never sees it. That property is architectural, and the
// cheapest way to lose it is a well-meaning change — a migration column "just
// to show the last digits", a template input that collects a card number
// directly, a FormValue wired up before anyone asked about PCI scope.
//
// So this test asserts the absence, over the artefacts where the mistake would
// actually land:
//
//   - migration SQL: a column whose name is a PAN/CVV/PIN shape;
//   - templates: an input bound to such a name;
//   - Go: a server-side form lookup for such a name.
//
// It is deliberately a name-shape check, not a data check: it cannot see a
// PAN that arrives as an opaque blob, and it does not try to. `pan_last4` (the
// only card-derived value we may ever store) is explicitly allowed, and the
// Interswitch iframe mount points (`id="cardNumber-container"`) are not form
// fields and so are not matched.
//
// A failure here is not a style complaint. It means either the change is wrong,
// or someone has decided to take on PCI DSS scope — and that decision belongs in
// CARD_ON_FILE_DECISION.md, not in a diff.
func TestNoCardholderDataShapes(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	var violations []string
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable corners are not this guard's business
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "reports", "node_modules", ".freebuff", ".opencode", "v-kaf":
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if filepath.Base(path) == "payment_card_data_guard_test.go" {
			return nil // this guard names the shapes it forbids
		}
		switch {
		case strings.HasSuffix(path, ".sql"):
			scanned++
			violations = append(violations, scanSQL(path, rel)...)
		case strings.HasSuffix(path, ".html"):
			scanned++
			violations = append(violations, scanTemplate(path, rel)...)
		case strings.HasSuffix(path, ".go"):
			scanned++
			violations = append(violations, scanGoFormLookups(path, rel)...)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
	// A walk that silently found nothing would make every assertion below
	// vacuous, which is how guards rot. The repository has well over a hundred
	// SQL, template and Go files; refusing to run below that keeps this honest.
	if scanned < 100 {
		t.Fatalf("only %d files scanned from %s — the guard is not looking where it thinks it is", scanned, root)
	}
	if len(violations) > 0 {
		t.Fatalf("cardholder-data shapes found (%d) — the platform stores no card data, and this is how that changes:\n  %s\n\n"+
			"If this is intentional, the PCI DSS scope change belongs in CARD_ON_FILE_DECISION.md first.",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// TestCardShapeClassifierHasTeeth pins the classifier itself. A guard is only
// worth its runtime if it can fail, so the names it must reject and the ones it
// must tolerate are both asserted here rather than assumed.
func TestCardShapeClassifierHasTeeth(t *testing.T) {
	t.Parallel()
	mustReject := []string{
		"pan", "card_number", "cardnumber", "card_no", "cc_num", "cc_number",
		"cvv", "cvv2", "cvc", "cvc2", "card_pin", "pin_block", "track1",
		"track2", "track_data", "magstripe", "security_code",
	}
	for _, name := range mustReject {
		if !cardColumnName.MatchString(name) {
			t.Errorf("classifier accepts %q, which is cardholder data — the guard would not catch it", name)
		}
	}
	mustTolerate := []string{
		"pan_last4", // the one permitted card-derived value
		"card_expiry_month", "card_brand", "amount_kobo", "account_number",
		"cardinal_container", "card_on_file_enabled", "pin_attempts",
	}
	for _, name := range mustTolerate {
		if cardColumnName.MatchString(name) {
			t.Errorf("classifier rejects %q, which carries no cardholder data — the guard would block legitimate work", name)
		}
	}
}

// cardColumnName matches a column or field named for cardholder data. Anchored,
// so `pan_last4` and `card_expiry_month` are not caught: the first is the only
// permitted card-derived value, the second is not sensitive.
var cardColumnName = regexp.MustCompile(`(?i)^(pan|card_?number|card_?no|cc_?num|cc_?number|cvv2?|cvc2?|card_?pin|pin_?block|track[12]|track_?data|magstripe|security_?code|card_?verification_?value)$`)

var (
	// sqlAddColumn strips the ALTER TABLE prefix so the column name is first.
	sqlAddColumn = regexp.MustCompile(`(?i)\bADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?`)
	// sqlColumnDef captures the leading identifier of a column definition line.
	sqlColumnDef = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s+(?:text|varchar|character|char|bytea|bigint|integer|int|numeric|jsonb|boolean|timestamptz|timestamp|date)\b`)
	// htmlNameAttr captures a form input's name attribute value.
	htmlNameAttr = regexp.MustCompile(`(?i)\bname\s*=\s*"([^"]+)"`)
	// goFormLookup captures the key of a server-side form lookup.
	goFormLookup = regexp.MustCompile(`(?i)\b(?:FormValue|PostFormValue|Form\.Get|Form\.PostForm|PostForm)\s*\(\s*"([^"]+)"`)
)

func scanSQL(path, rel string) []string {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for i, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		normalized := sqlAddColumn.ReplaceAllString(trimmed, "")
		m := sqlColumnDef.FindStringSubmatch(normalized)
		if len(m) < 2 {
			continue
		}
		if cardColumnName.MatchString(m[1]) {
			out = append(out, fmt.Sprintf("%s:%d: column %q", rel, i+1, m[1]))
		}
	}
	return out
}

func scanTemplate(path, rel string) []string {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for i, line := range strings.Split(string(body), "\n") {
		for _, m := range htmlNameAttr.FindAllStringSubmatch(line, -1) {
			if cardColumnName.MatchString(m[1]) {
				out = append(out, fmt.Sprintf("%s:%d: form field name=%q", rel, i+1, m[1]))
			}
		}
	}
	return out
}

func scanGoFormLookups(path, rel string) []string {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for i, line := range strings.Split(string(body), "\n") {
		for _, m := range goFormLookup.FindAllStringSubmatch(line, -1) {
			if cardColumnName.MatchString(m[1]) {
				out = append(out, fmt.Sprintf("%s:%d: form lookup %q", rel, i+1, m[1]))
			}
		}
	}
	return out
}

// repoRoot walks up from the test's working directory (the package directory)
// until it finds the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate the module root (go.mod) above the test directory")
		}
		dir = parent
	}
}
