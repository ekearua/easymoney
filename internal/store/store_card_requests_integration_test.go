package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestCardRequestLifecycle pins the physical-card invariants at the store
// layer:
//
//   - a request is opened for an active area, sealed at rest, and a second open
//     request for the same customer is refused;
//   - readiness is default-closed: all five gates must be evidenced before any
//     stock can be marked ready, and the agent must be active and in the
//     customer's area;
//   - a ready card is collected exactly once, with a salted code and a recorded
//     ID *type* (never a number);
//   - withdrawing a gate blocks the next handover.
func TestCardRequestLifecycle(t *testing.T) {
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
		TRUNCATE card_request_pickups, card_requests, card_issuer_readiness,
		         agent_users, agent_profiles, agent_regions, users, merchants
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	merchants, err := repository.ListActiveMerchants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(merchants) < 1 {
		t.Skip("needs at least one seeded active merchant")
	}
	agent := merchants[0]
	customer, err := repository.GetOrCreateUser(ctx, "+2348000000101")
	if err != nil {
		t.Fatal(err)
	}

	region, err := repository.EnsureAgentRegion(ctx, AgentRegion{
		Code: "oyo-ibadan", Name: "Ibadan", State: "Oyo", LGA: "Ibadan North",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SetMerchantType(ctx, agent.ID, MerchantTypeAgent); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureAgentProfile(ctx, agent.ID, region.ID, AgentTierStandard); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetAgentStatus(ctx, agent.ID, AgentStatusActive); err != nil {
		t.Fatal(err)
	}

	// Only areas with an active agent can be chosen, so a card is never
	// promised where nobody can hand it over.
	openRegions, err := repository.AgentRegionsWithActiveAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(openRegions) != 1 || openRegions[0].ID != region.ID {
		t.Fatalf("AgentRegionsWithActiveAgent = %v, want only %s", openRegions, region.ID)
	}
	agents, err := repository.ActiveAgentsInRegion(ctx, region.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].MerchantID != agent.ID {
		t.Fatalf("ActiveAgentsInRegion = %v, want %s", agents, agent.ID)
	}

	request, err := repository.CreateCardRequest(ctx, CreateCardRequestParams{
		UserID:         customer.ID,
		Channel:        "whatsapp",
		RegionID:       region.ID,
		CardholderName: "Ada Obi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != CardRequestRequested || request.CardholderName != "Ada Obi" || request.RegionName != "Ibadan" {
		t.Fatalf("unexpected created request: %+v", request)
	}
	if _, err := repository.CreateCardRequest(ctx, CreateCardRequestParams{
		UserID:         customer.ID,
		Channel:        "whatsapp",
		RegionID:       region.ID,
		CardholderName: "Ada Obi",
	}); !errors.Is(err, ErrCardRequestOpen) {
		t.Fatalf("second open request: want ErrCardRequestOpen, got %v", err)
	}
	if _, err := repository.CreateCardRequest(ctx, CreateCardRequestParams{
		UserID:         customer.ID,
		Channel:        "whatsapp",
		RegionID:       uuid.New(),
		CardholderName: "Ada Obi",
	}); !errors.Is(err, ErrCardRequestRegionInvalid) {
		t.Fatalf("unknown area: want ErrCardRequestRegionInvalid, got %v", err)
	}

	code, err := NewRemittanceCode()
	if err != nil {
		t.Fatal(err)
	}
	salt, err := NewRemittanceSalt()
	if err != nil {
		t.Fatal(err)
	}
	readyParams := MarkCardReadyParams{
		CardRequestID:   request.ID,
		IssuerReference: "ISW-CARD-0001",
		AgentMerchantID: agent.ID,
		CodeHash:        RemittanceCodeHash(salt, code),
		CodeSalt:        salt,
		ExpiresAt:       time.Now().Add(720 * time.Hour),
	}
	// A requested (unreviewed) card can never be marked ready.
	if _, err := repository.MarkCardRequestReady(ctx, readyParams); !errors.Is(err, ErrCardRequestInvalidState) {
		t.Fatalf("ready before review: want ErrCardRequestInvalidState, got %v", err)
	}
	approved, err := repository.ReviewCardRequest(ctx, request.ID, true, "KYC and area verified", "reviewer@xego.ng")
	if err != nil || approved.Status != CardRequestApproved {
		t.Fatalf("approve: status=%q err=%v", approved.Status, err)
	}
	if _, err := repository.ReviewCardRequest(ctx, request.ID, false, "second look", "reviewer@xego.ng"); !errors.Is(err, ErrCardRequestInvalidState) {
		t.Fatalf("double review: want ErrCardRequestInvalidState, got %v", err)
	}

	// Default-closed: no gate row means not ready.
	if readyNow, err := repository.CardIssuerReady(ctx); err != nil || readyNow {
		t.Fatalf("CardIssuerReady before attestation = %v err=%v, want false", readyNow, err)
	}
	if _, err := repository.MarkCardRequestReady(ctx, readyParams); !errors.Is(err, ErrCardIssuerNotReady) {
		t.Fatalf("ready with no gates: want ErrCardIssuerNotReady, got %v", err)
	}
	if err := repository.AttestCardIssuerGate(ctx, CardGatePCIScope, true, "", "no evidence", "reviewer@xego.ng"); err == nil {
		t.Fatal("a ready gate with no evidence reference must be refused")
	}
	for _, gate := range cardReadinessGates[:len(cardReadinessGates)-1] {
		if err := repository.AttestCardIssuerGate(ctx, gate, true, "EVID-"+gate, "", "reviewer@xego.ng"); err != nil {
			t.Fatal(err)
		}
	}
	if readyNow, err := repository.CardIssuerReady(ctx); err != nil || readyNow {
		t.Fatalf("CardIssuerReady with one gate missing = %v err=%v, want false", readyNow, err)
	}
	if _, err := repository.MarkCardRequestReady(ctx, readyParams); !errors.Is(err, ErrCardIssuerNotReady) {
		t.Fatalf("ready with four gates: want ErrCardIssuerNotReady, got %v", err)
	}
	if err := repository.AttestCardIssuerGate(ctx, CardGateFulfillment, true, "EVID-DELIVERY", "", "reviewer@xego.ng"); err != nil {
		t.Fatal(err)
	}
	if readyNow, err := repository.CardIssuerReady(ctx); err != nil || !readyNow {
		t.Fatalf("CardIssuerReady with all gates = %v err=%v, want true", readyNow, err)
	}

	// A stock handover needs an active agent in the customer's own area.
	if len(merchants) > 1 {
		otherRegion, err := repository.EnsureAgentRegion(ctx, AgentRegion{
			Code: "lagos-ikeja", Name: "Ikeja", State: "Lagos", LGA: "Ikeja",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.EnsureAgentProfile(ctx, merchants[1].ID, otherRegion.ID, AgentTierStandard); err != nil {
			t.Fatal(err)
		}
		if err := repository.SetAgentStatus(ctx, merchants[1].ID, AgentStatusActive); err != nil {
			t.Fatal(err)
		}
		mismatched := readyParams
		mismatched.AgentMerchantID = merchants[1].ID
		if _, err := repository.MarkCardRequestReady(ctx, mismatched); !errors.Is(err, ErrCardRequestRegionMismatch) {
			t.Fatalf("wrong-area agent: want ErrCardRequestRegionMismatch, got %v", err)
		}
	}

	ready, err := repository.MarkCardRequestReady(ctx, readyParams)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != CardRequestReady || ready.IssuerReference != "ISW-CARD-0001" || ready.AgentMerchantID == nil || *ready.AgentMerchantID != agent.ID {
		t.Fatalf("unexpected ready request: %+v", ready)
	}
	pending, err := repository.AgentCardRequests(ctx, agent.ID, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != request.ID {
		t.Fatalf("agent queue = %v err=%v", pending, err)
	}
	// A second handover of the same stock would be a duplicate request row.
	if _, err := repository.MarkCardRequestReady(ctx, readyParams); !errors.Is(err, ErrCardRequestInvalidState) {
		t.Fatalf("re-ready: want ErrCardRequestInvalidState, got %v", err)
	}

	// The ID type is mandatory; the number is never part of the call.
	if _, err := repository.CollectCardRequest(ctx, CollectCardParams{
		AgentMerchantID: agent.ID, OperatorUserID: uuid.New(), Code: code, IDType: "",
	}); !errors.Is(err, ErrCardIDTypeRequired) {
		t.Fatalf("collect without ID type: want ErrCardIDTypeRequired, got %v", err)
	}
	if _, err := repository.CollectCardRequest(ctx, CollectCardParams{
		AgentMerchantID: agent.ID, OperatorUserID: uuid.New(), Code: "000000", IDType: "nin",
	}); !errors.Is(err, ErrCardPickupInvalid) {
		t.Fatalf("wrong code: want ErrCardPickupInvalid, got %v", err)
	}

	operator := uuid.New()
	if err := repository.SetAgentStatus(ctx, agent.ID, AgentStatusSuspended); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CollectCardRequest(ctx, CollectCardParams{
		AgentMerchantID: agent.ID, OperatorUserID: operator, Code: code, IDType: "nin",
	}); !errors.Is(err, ErrAgentNotActive) {
		t.Fatalf("suspended agent collect: want ErrAgentNotActive, got %v", err)
	}
	if err := repository.SetAgentStatus(ctx, agent.ID, AgentStatusActive); err != nil {
		t.Fatal(err)
	}

	collected, err := repository.CollectCardRequest(ctx, CollectCardParams{
		AgentMerchantID: agent.ID, OperatorUserID: operator, Code: code, IDType: "nin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if collected.Status != CardRequestCollected || collected.CollectionIDType != "nin" ||
		collected.CollectedBy == nil || *collected.CollectedBy != operator || collected.CollectedAt == nil {
		t.Fatalf("unexpected collected request: %+v", collected)
	}
	// The code is single-use and the request is no longer ready.
	if _, err := repository.CollectCardRequest(ctx, CollectCardParams{
		AgentMerchantID: agent.ID, OperatorUserID: operator, Code: code, IDType: "nin",
	}); !errors.Is(err, ErrCardPickupInvalid) {
		t.Fatalf("second collect: want ErrCardPickupInvalid, got %v", err)
	}
	if _, err := repository.ReissueCardPickupCode(ctx, request.ID, RemittanceCodeHash(salt, "111111"), salt, time.Now().Add(time.Hour)); !errors.Is(err, ErrCardRequestInvalidState) {
		t.Fatalf("reissue on collected card: want ErrCardRequestInvalidState, got %v", err)
	}
	if queue, err := repository.AgentCardRequests(ctx, agent.ID, 10); err != nil || len(queue) != 0 {
		t.Fatalf("agent queue after collection = %v err=%v, want empty", queue, err)
	}

	// Withdrawing a gate blocks the next stock handover, even for an approved
	// request.
	second, err := repository.CreateCardRequest(ctx, CreateCardRequestParams{
		UserID:         customer.ID,
		Channel:        "telegram",
		RegionID:       region.ID,
		CardholderName: "Ada Obi",
	})
	if err != nil {
		t.Fatalf("a new request after collection must be allowed: %v", err)
	}
	if _, err := repository.ReviewCardRequest(ctx, second.ID, true, "ok", "reviewer@xego.ng"); err != nil {
		t.Fatal(err)
	}
	if err := repository.AttestCardIssuerGate(ctx, CardGateFulfillment, false, "EVID-DELIVERY", "courier contract lapsed", "reviewer@xego.ng"); err != nil {
		t.Fatal(err)
	}
	secondCode, _ := NewRemittanceCode()
	secondSalt, _ := NewRemittanceSalt()
	if _, err := repository.MarkCardRequestReady(ctx, MarkCardReadyParams{
		CardRequestID:   second.ID,
		IssuerReference: "ISW-CARD-0002",
		AgentMerchantID: agent.ID,
		CodeHash:        RemittanceCodeHash(secondSalt, secondCode),
		CodeSalt:        secondSalt,
		ExpiresAt:       time.Now().Add(time.Hour),
	}); !errors.Is(err, ErrCardIssuerNotReady) {
		t.Fatalf("withdrawn gate: want ErrCardIssuerNotReady, got %v", err)
	}
}
