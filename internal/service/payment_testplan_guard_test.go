package service

// Drift guard for reports/Payment_Pipeline_Test_Plan.xlsx. The workbook is a
// local, gitignored artifact (/reports/ is ignored), so it cannot be the
// source of truth — this file is. Every scenario row in the workbook's
// "Existing Test Coverage" sheet must map to a test function that exists in
// the tree; when a test is renamed or deleted without updating this table,
// TestPaymentPlanScenariosResolve fails and the workbook goes stale loudly.
//
// To refresh the workbook after moving tests, run:
//
//	python scripts/update_test_plan_coverage.py
//
// (after updating NEW_ROWS there to match this table).

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// planScenario is one coverage claim: scenario ID, the test functions that
// cover it (name -> relative file), and whether the claim is informational
// only (documented patterns that are not single named functions).
type planScenario struct {
	id     string
	desc   string
	tests  map[string]string // test function name -> file containing it
	region string            // file:line hint for humans; not machine-checked
}

// paymentPlanScenarios is the canonical scenario -> coverage mapping, kept in
// lockstep with scripts/update_test_plan_coverage.py's NEW_ROWS.
var paymentPlanScenarios = []planScenario{
	{
		id:     "C1",
		desc:   "Merchant payment success (card checkout)",
		tests:  map[string]string{"TestSimulateMakePaymentAndPayIndividual": "internal/app/simulate_flows_test.go"},
		region: "simulate_flows_test.go:235",
	},
	{
		id:     "C1 (live)",
		desc:   "Live Interswitch requery path (gated smoke)",
		tests:  map[string]string{"TestLiveInterswitchPaymentLedger": "internal/app/live_interswitch_ledger_test.go"},
		region: "live_interswitch_ledger_test.go:43",
	},
	{
		id:     "C2",
		desc:   "Card payment declined",
		tests:  map[string]string{"TestCardPaymentDeclined_C2": "internal/service/payment_pipeline_test.go"},
		region: "payment_pipeline_test.go:176",
	},
	{
		id:     "C3",
		desc:   "Card payment expired (stale requery)",
		tests:  map[string]string{"TestCardCheckoutExpiry_C3": "internal/service/payment_expiry_test.go"},
		region: "payment_expiry_test.go:49",
	},
	{
		id:     "C4",
		desc:   "Card fee validation (200bps + 10000 flat, 350000 cap)",
		tests:  map[string]string{"TestSimulateMakePaymentAndPayIndividual": "internal/app/simulate_flows_test.go"},
		region: "fees.go; asserted via ledger splits in the simulate suite",
	},
	{
		id:     "C5",
		desc:   "Webhook-driven card success (idempotent)",
		tests:  map[string]string{"TestCardPaymentWebhookIdempotent_C5": "internal/service/payment_pipeline_test.go"},
		region: "payment_pipeline_test.go:205",
	},
	{
		id:     "C6",
		desc:   "Invoice payment via card",
		tests:  map[string]string{"TestInvoicePaymentViaCard_C6": "internal/service/payment_pipeline_test.go"},
		region: "payment_pipeline_test.go:250",
	},
	{
		id:     "B1",
		desc:   "Simulated bank transfer success",
		tests:  map[string]string{"TestReconciliationThreeWay": "internal/store/store_integration_test.go"},
		region: "store_integration_test.go:1409 (drives ConfirmBankTransferSimulation)",
	},
	{
		id:     "B2",
		desc:   "Simulated bank transfer expiry",
		tests:  map[string]string{"TestBankTransferExpiry_B2": "internal/service/payment_expiry_test.go"},
		region: "payment_expiry_test.go:100",
	},
	{
		id:     "B3",
		desc:   "Interswitch DVA success",
		tests:  map[string]string{"TestPostgresVirtualAccountInstructionRoundTrip": "internal/store/store_integration_test.go"},
		region: "store_integration_test.go:18",
	},
	{
		id:     "B4",
		desc:   "DVA fee validation (150bps, 0 fixed, 150000 cap)",
		tests:  map[string]string{"TestReconciliationThreeWay": "internal/store/store_integration_test.go"},
		region: "fees.go; asserted via ledger double-entry assertions",
	},
	{
		id:     "B5",
		desc:   "Individual pay via bank transfer (NIP)",
		tests:  map[string]string{"TestSimulateMakePaymentAndPayIndividual": "internal/app/simulate_flows_test.go"},
		region: "simulate_flows_test.go:235 (individual-pay branch)",
	},
	{
		id:   "W1",
		desc: "Wallet payment success (sufficient balance)",
		tests: map[string]string{
			"TestReproWalletWebPayment":  "internal/app/repro_wallet_web_test.go",
			"TestWalletPaymentAndRefund": "internal/store/store_integration_test.go",
		},
		region: "repro_wallet_web_test.go",
	},
	{
		id:   "W2",
		desc: "Wallet payment insufficient balance",
		tests: map[string]string{
			"TestFriendlyWebPaymentError":          "internal/app/webflow_media_test.go",
			"TestFriendlyWebPaymentErrorAllowance": "internal/app/webflow_media_test.go",
		},
		region: "webflow_media_test.go:85; ConfirmWalletPayment/transitionPayment guard",
	},
	{
		id:     "W3",
		desc:   "Wallet topup via card",
		tests:  map[string]string{"TestWalletTopup": "internal/store/store_integration_test.go"},
		region: "store_integration_test.go",
	},
	{
		id:     "W4",
		desc:   "Wallet topup via bank transfer",
		tests:  map[string]string{"TestWalletTopup": "internal/store/store_integration_test.go"},
		region: "store_integration_test.go (shares the topup ledger path)",
	},
	{
		id:   "W5",
		desc: "Wallet on inactive account",
		tests: map[string]string{
			"TestFriendlyWebPaymentError":          "internal/app/webflow_media_test.go",
			"TestFriendlyWebPaymentErrorAllowance": "internal/app/webflow_media_test.go",
		},
		region: "webflow_media_test.go:92; wfWalletStatePreview guard",
	},
	{
		id:   "R1",
		desc: "Full refund (maker-checker)",
		tests: map[string]string{
			"TestRefundLifecycle":        "internal/store/store_refunds_integration_test.go",
			"TestWalletPaymentAndRefund": "internal/store/store_integration_test.go",
		},
		region: "store_refunds_integration_test.go",
	},
	{
		id:     "R2",
		desc:   "Partial refund",
		tests:  map[string]string{"TestRefundLifecycle": "internal/store/store_refunds_integration_test.go"},
		region: "store_refunds_integration_test.go (partial subtest)",
	},
	{
		id:     "R3",
		desc:   "Duplicate refund blocked",
		tests:  map[string]string{"TestRefundLifecycle": "internal/store/store_refunds_integration_test.go"},
		region: "store_refunds_integration_test.go (dup-guard subtests)",
	},
	{
		id:     "R4",
		desc:   "Settlement cut and payout",
		tests:  map[string]string{"TestSettlementLifecycle": "internal/store/store_settlements_integration_test.go"},
		region: "store_settlements_integration_test.go",
	},
	{
		id:     "R5",
		desc:   "Settlement payout failure and retry",
		tests:  map[string]string{"TestSettlementLifecycle": "internal/store/store_settlements_integration_test.go"},
		region: "store_settlements_integration_test.go (fail/retry subtests)",
	},
}

// collectTestFunctions parses every *_test.go under the repo root and returns
// the set of test function names. It walks upward from this file's directory
// to find the module root (the directory containing go.mod).
func collectTestFunctions(t *testing.T) map[string]bool {
	t.Helper()
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("could not locate module root (go.mod) from test directory")
		}
		root = parent
	}

	tests := map[string]bool{}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "web" || name == "reports" || name == "scripts" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if strings.HasPrefix(fn.Name.Name, "Test") && strings.HasSuffix(fn.Name.Name, "Testing") == false {
				// Standard testing func: func TestXxx(t *testing.T).
				if sig, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr); ok {
					if sel, ok := sig.X.(*ast.SelectorExpr); ok {
						if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "testing" && sel.Sel.Name == "T" {
							tests[fn.Name.Name] = true
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tests
}

// TestPaymentPlanScenariosResolve fails when a scenario in the test plan maps
// to a test function that no longer exists, so the coverage sheet can never
// silently drift from the suite.
func TestPaymentPlanScenariosResolve(t *testing.T) {
	tests := collectTestFunctions(t)
	if len(tests) < 50 {
		t.Fatalf("suspiciously few test functions collected (%d); the walker may be broken", len(tests))
	}

	var missing []string
	for _, sc := range paymentPlanScenarios {
		for name, file := range sc.tests {
			if !tests[name] {
				missing = append(missing, fmt.Sprintf("%s (%s): test %s not found; expected in %s — update paymentPlanScenarios and scripts/update_test_plan_coverage.py",
					sc.id, sc.desc, name, file))
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("payment test plan drifted from the suite:\n  %s", strings.Join(missing, "\n  "))
	}
}
