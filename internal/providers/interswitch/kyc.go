// Package interswitch implements Quickteller KYC/verification adapters.
package interswitch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
)

type KYCClient struct {
	baseURL      string
	clientID     string
	clientSecret string
	httpClient   *http.Client
}

func NewKYCClient(baseURL, clientID, clientSecret string, timeout time.Duration) *KYCClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://sandbox.interswitchng.com"
	}
	return &KYCClient{
		baseURL:      base,
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		httpClient:   &http.Client{Timeout: timeout},
	}
}

func (k *KYCClient) ProviderName() string { return "interswitch" }

type verifySubRequest struct {
	Type           string `json:"type"`
	IdentityNumber string `json:"identityNumber,omitempty"`
}

type verifyRequest struct {
	Type                 string             `json:"type"`
	FirstName            string             `json:"firstName,omitempty"`
	LastName             string             `json:"lastName,omitempty"`
	Phone                string             `json:"phone,omitempty"`
	BirthDate            string             `json:"birthDate,omitempty"`
	RegisteredName       string             `json:"registeredName,omitempty"`
	VerificationRequests []verifySubRequest `json:"verificationRequests"`
	CallbackURL          string             `json:"callbackUrl,omitempty"`
}

type verifyResponse struct {
	Type           string `json:"type"`
	Status         string `json:"status"`
	IdentityNumber string `json:"identityNumber"`
	FirstName      string `json:"firstName"`
	LastName       string `json:"lastName"`
	BirthDate      string `json:"birthDate"`
	RegisteredName string `json:"registeredName"`
	AMLMatches     []struct {
		Categories []string `json:"categories"`
	} `json:"amlMatches"`
	Reference string `json:"reference"`
}

func (k *KYCClient) call(ctx context.Context, req verifyRequest) (verifyResponse, error) {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, k.baseURL+"/api/v1/verification", bytes.NewReader(body))
	if err != nil {
		return verifyResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Client-Id", k.clientID)
	httpReq.Header.Set("Client-Secret", k.clientSecret)
	resp, err := k.httpClient.Do(httpReq)
	if err != nil {
		return verifyResponse{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return verifyResponse{}, fmt.Errorf("interswitch verification HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out verifyResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return verifyResponse{}, err
	}
	return out, nil
}

func isVerified(status string) bool {
	s := strings.ToUpper(strings.TrimSpace(status))
	return s == "VERIFIED" || s == "MATCH"
}

func mapStatus(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "VERIFIED", "MATCH":
		return ports.VerifyVerified
	case "NOT VERIFIED", "NO MATCH":
		return ports.VerifyMismatch
	case "FAILED":
		return ports.VerifyNotFound
	default:
		return ports.VerifyReview
	}
}

func (k *KYCClient) VerifyIdentity(ctx context.Context, request ports.IdentityVerificationRequest) (ports.IdentityVerificationResult, error) {
	idType := strings.ToUpper(strings.TrimSpace(request.IDType))
	req := verifyRequest{
		Type:       "INDIVIDUAL",
		FirstName:  request.LegalName,
		Phone:      request.PhoneNumber,
		BirthDate:  request.DateOfBirth,
		VerificationRequests: []verifySubRequest{{Type: idType, IdentityNumber: request.IDNumber}},
	}
	out, err := k.call(ctx, req)
	if err != nil {
		return ports.IdentityVerificationResult{}, err
	}
	res := ports.IdentityVerificationResult{
		Status:      mapStatus(out.Status),
		ProviderRef: out.Reference,
		MatchName:   isVerified(out.Status) && (request.LegalName == "" || strings.EqualFold(out.LastName+" "+out.FirstName, request.LegalName)),
		MatchDOB:    isVerified(out.Status) && (request.DateOfBirth == "" || out.BirthDate == request.DateOfBirth),
		Message:     out.Status,
	}
	return res, nil
}

func (k *KYCClient) VerifyCorporate(ctx context.Context, request ports.CorporateRegistryRequest) (ports.CorporateRegistryResult, error) {
	req := verifyRequest{
		Type:           "CORPORATE",
		RegisteredName: request.BusinessName,
		VerificationRequests: []verifySubRequest{{Type: "CORPORATE_REGISTRATION", IdentityNumber: request.RegistrationNumber}},
	}
	out, err := k.call(ctx, req)
	if err != nil {
		return ports.CorporateRegistryResult{}, err
	}
	match := isVerified(out.Status)
	if request.BusinessName != "" {
		match = match && strings.EqualFold(strings.TrimSpace(out.RegisteredName), strings.TrimSpace(request.BusinessName))
	}
	return ports.CorporateRegistryResult{
		Status:         mapStatus(out.Status),
		RegisteredName: out.RegisteredName,
		ProviderRef:    out.Reference,
		MatchName:      match,
		Message:        out.Status,
	}, nil
}

func (k *KYCClient) Screen(ctx context.Context, request ports.ScreeningRequest) (ports.ScreeningDecision, error) {
	req := verifyRequest{
		Type:      "INDIVIDUAL",
		FirstName: request.LegalName,
		Phone:     request.PhoneNumber,
		VerificationRequests: []verifySubRequest{{Type: "AML"}},
	}
	out, err := k.call(ctx, req)
	if err != nil {
		return ports.ScreeningDecision{}, err
	}
	decision := kyc.ScreenClear
	if len(out.AMLMatches) > 0 {
		decision = kyc.ScreenPossible
	}
	matched := []string{}
	for _, m := range out.AMLMatches {
		matched = append(matched, strings.Join(m.Categories, ","))
	}
	return ports.ScreeningDecision{
		Decision:     decision,
		MatchedNames: matched,
		ProviderRef:  out.Reference,
		Message:      out.Status,
	}, nil
}

func (k *KYCClient) ScreenAdverseMedia(ctx context.Context, request ports.AdverseMediaRequest) (ports.AdverseMediaDecision, error) {
	scr, err := k.Screen(ctx, ports.ScreeningRequest{LegalName: request.LegalName, PhoneNumber: request.PhoneNumber})
	if err != nil {
		return ports.AdverseMediaDecision{}, err
	}
	return ports.AdverseMediaDecision{
		Decision:    scr.Decision,
		ProviderRef: scr.ProviderRef,
		Message:     scr.Message,
	}, nil
}

func (k *KYCClient) VerifyBusinessKYC(ctx context.Context, request ports.BusinessKYCRequest) (ports.BusinessKYCResult, error) {
	corp, err := k.VerifyCorporate(ctx, ports.CorporateRegistryRequest{RegistrationNumber: request.RegistrationNumber, BusinessName: request.BusinessName})
	if err != nil {
		return ports.BusinessKYCResult{}, err
	}
	scr, err := k.Screen(ctx, ports.ScreeningRequest{LegalName: request.BusinessName, PhoneNumber: request.Phone})
	if err != nil {
		return ports.BusinessKYCResult{}, err
	}
	return ports.BusinessKYCResult{
		Status:         corp.Status,
		RegisteredName: corp.RegisteredName,
		RegisteredAt:   corp.RegisteredAt,
		ProviderRef:    corp.ProviderRef,
		MatchName:      corp.MatchName,
		AMLDecision:    scr.Decision,
		AMLMatches:     scr.MatchedNames,
		Message:        corp.Message,
	}, nil
}

// VerifyTaxID is not supported by the Interswitch verification endpoint: it
// validates BVN, NIN, driver's licence, passport, corporate registration,
// address, AML and bank account only. A TIN lookup through this rail is
// rejected as an error so a misconfigured FIRS_PROVIDER=interswitch fails
// closed instead of silently returning a corporate-registry verdict.
func (k *KYCClient) VerifyTaxID(_ context.Context, _ ports.TaxIDRequest) (ports.TaxIDResult, error) {
	return ports.TaxIDResult{}, errors.New("interswitch verification endpoint does not support TIN (FIRS) lookups; use FIRS_PROVIDER=simulated or http")
}

// ScreenPayment reuses the AML screen for payment-time counterparty checks.
// The endpoint is the same; the only difference is the caller's expectation
// (hold vs interdict), which the service layer enforces from the decision.
func (k *KYCClient) ScreenPayment(ctx context.Context, req ports.PaymentScreenRequest) (ports.PaymentScreenDecision, error) {
	if strings.TrimSpace(req.CounterpartyName) == "" {
		return ports.PaymentScreenDecision{}, errors.New("counterparty name is required for payment screening")
	}
	scr, err := k.Screen(ctx, ports.ScreeningRequest{LegalName: req.CounterpartyName})
	if err != nil {
		return ports.PaymentScreenDecision{}, err
	}
	return ports.PaymentScreenDecision{
		Decision:     scr.Decision,
		MatchedNames: scr.MatchedNames,
		ProviderRef:  scr.ProviderRef,
		ListVersion:  "",
		Message:      scr.Message,
	}, nil
}
