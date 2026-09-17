package app

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
)

// The simulation rail providers were retired; these hermetic test doubles
// carry the deterministic provider contracts (failed/fulfilled data orders,
// verified/not_found identity outcomes, strong/possible/clear screening) so
// flow tests exercise the same boundaries without network access.

// stubDataProvider fulfils data orders locally for tests. A provider SKU
// containing "FAIL" fails; everything else is fulfilled.
type stubDataProvider struct{}

func (stubDataProvider) FulfilData(_ context.Context, request ports.DataFulfilmentRequest) (ports.DataFulfilmentResult, error) {
	if strings.Contains(strings.ToUpper(request.ProviderSKU), "FAIL") {
		return ports.DataFulfilmentResult{Status: "failed", Message: "provider failure"}, nil
	}
	return ports.DataFulfilmentResult{
		ProviderReference: "SIM-DATA-" + request.RequestCode,
		Status:            "fulfilled",
		Message:           fmt.Sprintf("%s %s delivered to %s", request.NetworkCode, request.PlanCode, request.BeneficiaryPhone),
	}, nil
}

func (stubDataProvider) CheckDataStatus(_ context.Context, providerReference string) (ports.DataFulfilmentResult, error) {
	if strings.TrimSpace(providerReference) == "" {
		return ports.DataFulfilmentResult{Status: "pending", Message: "no provider reference yet"}, nil
	}
	return ports.DataFulfilmentResult{ProviderReference: providerReference, Status: "fulfilled", Message: "fulfilled"}, nil
}

var stubDigits11 = regexp.MustCompile(`^[0-9]{11}$`)

// stubIdentityVerifier verifies NIN/BVN deterministically: an ID starting
// with 9 is not found, starting with 8 is a record mismatch, otherwise
// verified.
type stubIdentityVerifier struct{}

func (stubIdentityVerifier) VerifyIdentity(_ context.Context, request ports.IdentityVerificationRequest) (ports.IdentityVerificationResult, error) {
	number := strings.TrimSpace(request.IDNumber)
	idType := strings.ToLower(request.IDType)
	if idType != "nin" && idType != "bvn" {
		return ports.IdentityVerificationResult{}, fmt.Errorf("unsupported id type %q", request.IDType)
	}
	if !stubDigits11.MatchString(number) {
		return ports.IdentityVerificationResult{}, fmt.Errorf("%s must be exactly 11 digits", strings.ToUpper(idType))
	}
	status := "verified"
	message := fmt.Sprintf("%s verified", strings.ToUpper(idType))
	switch number[0] {
	case '9':
		status = "not_found"
		message = fmt.Sprintf("no %s record matched", strings.ToUpper(idType))
	case '8':
		status = "mismatch"
		message = "record matched but name or date of birth differ"
	}
	return ports.IdentityVerificationResult{
		Status:      status,
		ProviderRef: "SIM-" + strings.ToUpper(idType) + "-" + number,
		MatchName:   status == "verified" && strings.TrimSpace(request.LegalName) != "",
		MatchDOB:    status == "verified" && strings.TrimSpace(request.DateOfBirth) != "",
		Message:     message,
	}, nil
}

// stubSanctionsScreener screens names with the retired simulator rules:
// blocked names are a strong match, PEP tokens a possible match, otherwise
// clear.
type stubSanctionsScreener struct{}

func (stubSanctionsScreener) Screen(_ context.Context, request ports.ScreeningRequest) (ports.ScreeningDecision, error) {
	name := strings.ToLower(strings.TrimSpace(request.LegalName))
	if name == "" {
		return ports.ScreeningDecision{}, fmt.Errorf("legal name is required for screening")
	}
	blocked := []string{"sanctioned", "terror", "money launder", "drug lord", "pep high risk"}
	var matched []string
	for _, token := range blocked {
		if strings.Contains(name, token) {
			matched = append(matched, token)
		}
	}
	if len(matched) > 0 {
		return ports.ScreeningDecision{
			Decision:     kyc.ScreenStrong,
			MatchedNames: matched,
			ProviderRef:  "SIM-SCR-" + stubShortHash(name),
			Message:      "high-confidence sanctions match detected",
		}, nil
	}
	pepTokens := []string{"senator", "minister", "governor", "pep"}
	for _, token := range pepTokens {
		if strings.Contains(name, token) {
			matched = append(matched, token)
		}
	}
	if len(matched) > 0 {
		return ports.ScreeningDecision{
			Decision:     kyc.ScreenPossible,
			MatchedNames: matched,
			ProviderRef:  "SIM-SCR-" + stubShortHash(name),
			Message:      "potential PEP match detected",
		}, nil
	}
	return ports.ScreeningDecision{
		Decision:    kyc.ScreenClear,
		ProviderRef: "SIM-SCR-" + stubShortHash(name),
		Message:     "no sanctions or PEP matches",
	}, nil
}

func stubShortHash(value string) string {
	sum := 0
	for _, r := range value {
		sum = (sum*31 + int(r)) % 1000000
	}
	return fmt.Sprintf("%06d", sum)
}

// stubAI is a keyword-based test AI implementing the chat, image, and speech
// boundaries identically to the retired simulated provider.
type stubAI struct{}

func (stubAI) ClassifyIntent(_ context.Context, userMessage string, _ []string) (ports.IntentResult, error) {
	lower := strings.ToLower(strings.TrimSpace(userMessage))
	switch {
	case strings.Contains(lower, "pay") || strings.Contains(lower, "send money"):
		return ports.IntentResult{Intent: "pay", Confidence: 0.9, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "data") || strings.Contains(lower, "buy data"):
		return ports.IntentResult{Intent: "buy_data", Confidence: 0.85, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "invoice"):
		return ports.IntentResult{Intent: "invoice", Confidence: 0.85, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "thrift"):
		return ports.IntentResult{Intent: "thrift", Confidence: 0.85, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "individual") || strings.Contains(lower, "become"):
		return ports.IntentResult{Intent: "become_individual", Confidence: 0.8, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "nin") || strings.Contains(lower, "bvn") || strings.Contains(lower, "verify"):
		return ports.IntentResult{Intent: "verify_id", Confidence: 0.8, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "status") || strings.Contains(lower, "check"):
		return ports.IntentResult{Intent: "status", Confidence: 0.7, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "help"):
		return ports.IntentResult{Intent: "help", Confidence: 0.9, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "menu"):
		return ports.IntentResult{Intent: "menu", Confidence: 0.95, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "cancel"):
		return ports.IntentResult{Intent: "cancel", Confidence: 0.9, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "skip"):
		return ports.IntentResult{Intent: "skip", Confidence: 0.9, Entities: map[string]string{}}, nil
	default:
		return ports.IntentResult{Intent: "none", Confidence: 0}, nil
	}
}

func (stubAI) Answer(_ context.Context, question string, _ []string) (string, error) {
	lower := strings.ToLower(question)
	switch {
	case strings.Contains(lower, "how") || strings.Contains(lower, "what"):
		return "Xego lets you send money, buy data, create invoices, and join thrift groups — all from WhatsApp. Type MENU to see your options.", nil
	default:
		return "I'm here to help! You can type MENU to see your options, or ask me anything about Xego.", nil
	}
}

func (stubAI) ReadImage(_ context.Context, _ []byte, _, prompt string) (string, error) {
	if strings.Contains(strings.ToLower(prompt), "nin") || strings.Contains(strings.ToLower(prompt), "bvn") {
		return "NIN: 12345678901", nil
	}
	return "Payment reference: REF-12345678 Amount: NGN 5,000 Date: 2026-01-15", nil
}

func (stubAI) Transcribe(_ context.Context, _ []byte, _, _ string) (string, error) {
	return "pay 5000 to jumia", nil
}
