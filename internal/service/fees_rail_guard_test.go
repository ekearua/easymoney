package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"whatsapp-payment-demo/internal/config"
)

// TestDeclaredPaymentRailsPriceNonZero is the guard behind the saved-methods
// design decision (docs/payments/SAVED_CARD_METHODS_PLAN.md §3).
//
// Two defaults line up to make a new rail fail *silently*:
//
//   - FeeChannelForProvider maps a rail to the fee channel that prices it, and
//     its `default:` branch hands the rail straight back;
//   - XegoCollectionFee returns a zero fee for any channel it does not know.
//
// So a freshly declared rail does not fail to compile, does not panic and does
// not error — it bills the merchant nothing, on every payment, while every log
// line and status looks perfectly healthy. That is the one failure mode in this
// area that is invisible in production, so it gets a test instead of a
// convention.
//
// The test reads the Provider* string constants out of this package's own
// source rather than a hand-maintained list, so a rail added next quarter is
// checked without anyone remembering to add it here.
func TestDeclaredPaymentRailsPriceNonZero(t *testing.T) {
	t.Parallel()
	rails := providerStringConstants(t)
	if len(rails) == 0 {
		t.Fatal("no Provider* string constants found in the service package; the guard has lost its input and would pass vacuously")
	}

	// Every fee parameter non-zero, so a channel that is genuinely priced can
	// never be mistaken for one that is not.
	cfg := config.Config{
		FeeCardBPS: 150, FeeCardFixedKobo: 10_000, FeeCardCapKobo: 200_000,
		FeeDVABPS: 100, FeeDVAFixedKobo: 5_000, FeeDVACapKobo: 100_000,
		FeeTransferBPS: 50, FeeTransferFixedKobo: 2_500, FeeTransferCapKobo: 50_000,
	}
	const amountKobo = int64(1_000_000) // ₦10,000

	for name, rail := range rails {
		if IsExemptProviderValue(rail) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			if !IsDeclaredPaymentRail(rail) {
				t.Fatalf("%s (%q) is a provider constant but not a declared payment rail; "+
					"add it to DeclaredPaymentRails, or mark it exempt if it is not a rail at all", name, rail)
			}
			channel := FeeChannelForProvider(rail)
			if channel == rail {
				t.Fatalf("%s (%q) falls through FeeChannelForProvider's default branch (returned %q unchanged); "+
					"it must map to a real fee channel or it prices at zero", name, rail, channel)
			}
			if fee := XegoCollectionFee(cfg, channel, amountKobo).FeeKobo; fee <= 0 {
				t.Fatalf("%s (%q) maps to fee channel %q, which prices at %d kobo — a rail that bills nothing "+
					"is the silent-revenue-leak failure this guard exists for", name, rail, channel, fee)
			}
		})
	}
}

// TestRailGuardHasTeeth proves the guard is not vacuous by feeding it a rail
// that has no fee mapping — the exact shape of a future mistake. If this ever
// stops failing, the guard above has stopped protecting anything.
func TestRailGuardHasTeeth(t *testing.T) {
	t.Parallel()
	const invented = "card_on_file"
	if IsDeclaredPaymentRail(invented) {
		t.Fatalf("%q is already a declared rail; pick another name for this probe", invented)
	}
	mapped := FeeChannelForProvider(invented)
	if mapped != invented {
		t.Fatalf("probe rail unexpectedly mapped to %q; the guard's premise (unmapped rails pass through) no longer holds", mapped)
	}
	if fee := XegoCollectionFee(config.Config{FeeCardBPS: 150, FeeCardFixedKobo: 10_000}, mapped, 1_000_000).FeeKobo; fee != 0 {
		t.Fatalf("probe rail priced at %d kobo; the guard's premise (unknown channels price at zero) no longer holds", fee)
	}
}

// providerStringConstants parses the service package's non-test sources and
// returns every `Provider* = "..."` constant, name to value.
func providerStringConstants(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	// nil filter: parse every file in the package directory, including test
	// files, which are skipped by filename below.
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse service package: %v", err)
	}
	out := map[string]string{}
	for _, pkg := range pkgs {
		for filename, file := range pkg.Files {
			if len(filename) > 8 && filename[len(filename)-8:] == "_test.go" {
				continue
			}
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, ident := range value.Names {
						if i >= len(value.Values) {
							break
						}
						if len(ident.Name) < 8 || ident.Name[:8] != "Provider" {
							continue
						}
						lit, ok := value.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						rail, err := strconv.Unquote(lit.Value)
						if err != nil {
							continue
						}
						out[ident.Name] = rail
					}
				}
			}
		}
	}
	return out
}
