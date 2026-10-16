package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// AgentRemittanceService is the agent-facing half of the cash-pickup remittance
// (POS_AGENT_NETWORK_PLAN.md §9). It screens the recipient, then delegates the
// authorisation and money movement to store.RedeemPickupTx — the single choke
// point — and renders the receipt the CBN requires (agent name, region,
// coordinates). It holds no money-moving logic of its own: if this service is
// bypassed, no payout can happen; if the store is bypassed, none can either.
type AgentRemittanceService struct {
	cfg          config.Config
	store        *store.Store
	screener     ports.SanctionsScreener
	screenerName string
}

// NewAgentRemittanceService constructs the agent redemption workflow. A nil
// screener records an explicit clear decision (development/simulated posture);
// production wires the same sanctions provider the individual flow uses.
func NewAgentRemittanceService(cfg config.Config, repository *store.Store, screener ports.SanctionsScreener) *AgentRemittanceService {
	name := "simulated"
	if screener != nil {
		if namer, ok := screener.(interface{ ProviderName() string }); ok {
			name = namer.ProviderName()
		}
	}
	return &AgentRemittanceService{cfg: cfg, store: repository, screener: screener, screenerName: name}
}

// RedeemRequest is an agent operator's redemption of a cash pickup.
type RedeemRequest struct {
	AgentMerchantID  uuid.UUID
	OperatorUserID   *uuid.UUID
	Code             string
	OTP              string
	ReceiverIDType   string
	ReceiverIDNumber string
	GeoLat           *float64
	GeoLng           *float64
}

// RedeemReceipt is the evidence a redemption produced, including the printable
// text (agent name + region + coordinates) the CBN agent-banking guidelines
// require on a cash-out receipt.
type RedeemReceipt struct {
	Remittance  store.Remittance
	Payout      store.RemittancePayout
	AgentName   string
	RegionName  string
	AmountText  string
	ReceiptText string
}

// Redeem screens the recipient, then authorises the payout through the store
// choke point and assembles the receipt. A refused redemption returns the
// store's sentinel error unchanged, so the console can render the exact reason.
func (s *AgentRemittanceService) Redeem(ctx context.Context, req RedeemRequest) (RedeemReceipt, error) {
	if !s.cfg.AgentNetworkEnabled {
		return RedeemReceipt{}, errors.New("cash pickup at an agent is currently unavailable")
	}
	if req.AgentMerchantID == uuid.Nil {
		return RedeemReceipt{}, errors.New("redemption requires an agent")
	}

	// Display lookup (not the authority): resolve the remittance so the
	// recipient can be screened and the receipt names the agent/region.
	rem, err := s.store.RemittanceForAgentCode(ctx, req.AgentMerchantID, req.Code)
	if err != nil {
		return RedeemReceipt{}, err
	}
	decision, err := s.screenRecipient(ctx, rem)
	if err != nil {
		return RedeemReceipt{}, err
	}

	res, err := s.store.RedeemPickupTx(ctx, store.RedeemPickupParams{
		AgentMerchantID:       req.AgentMerchantID,
		OperatorUserID:        req.OperatorUserID,
		Code:                  req.Code,
		OTP:                   req.OTP,
		RequireOTP:            true,
		RequireID:             s.cfg.AgentIDRequired,
		ReceiverIDType:        req.ReceiverIDType,
		ReceiverIDNumber:      req.ReceiverIDNumber,
		IDHashPepper:          s.cfg.AgentIDHashPepper,
		ScreenDecision:        decision,
		CustomerDailyCapKobo:  config.CBNMaxCustomerDailyCashOutKobo,
		CustomerWeeklyCapKobo: config.CBNMaxCustomerWeeklyCashOutKobo,
		GeoLat:                req.GeoLat,
		GeoLng:                req.GeoLng,
	})
	if err != nil {
		return RedeemReceipt{}, err
	}
	return s.buildReceipt(ctx, res)
}

// Receipt loads the evidence for a completed payout so the console can render
// (and re-render) the printable receipt. It refuses a remittance that belongs to
// another agent, so a receipt URL cannot leak across terminals.
func (s *AgentRemittanceService) Receipt(ctx context.Context, agentMerchantID, remittanceID uuid.UUID) (RedeemReceipt, error) {
	rem, err := s.store.RemittanceByID(ctx, remittanceID)
	if err != nil {
		return RedeemReceipt{}, err
	}
	if rem.AgentMerchantID != agentMerchantID {
		return RedeemReceipt{}, errors.New("receipt belongs to another agent")
	}
	payout, err := s.store.RemittancePayoutByRemittance(ctx, remittanceID)
	if err != nil {
		return RedeemReceipt{}, err
	}
	return s.buildReceipt(ctx, store.RedeemPickupResult{Remittance: rem, Payout: payout})
}

// screenRecipient records (or reuses) the counterparty screen for a remittance.
// The recorded row is the authoritative decision; a re-attempt reuses it rather
// than re-screening, mirroring the payout rail.
func (s *AgentRemittanceService) screenRecipient(ctx context.Context, rem store.Remittance) (string, error) {
	ref := "remittance:" + rem.ID.String()
	if recorded, found, err := s.store.PaymentScreeningForRef(ctx, store.RailCashPickup, ref); err != nil {
		return "", err
	} else if found {
		return recorded.Decision, nil
	}

	decision, matched, providerRef, listVersion, message := kyc.ScreenClear, []string(nil), "", "", "screening not configured"
	if s.screener != nil {
		result, err := s.screener.Screen(ctx, ports.ScreeningRequest{
			LegalName:   rem.RecipientName,
			PhoneNumber: rem.RecipientPhone,
		})
		if err != nil {
			return "", err
		}
		decision, matched = result.Decision, result.MatchedNames
		providerRef, listVersion, message = result.ProviderRef, result.ListVersion, result.Message
	}
	recorded, err := s.store.RecordPaymentScreening(ctx, store.PaymentScreening{
		Rail:             store.RailCashPickup,
		MovementRef:      ref,
		SubjectID:        rem.SenderUserID,
		AmountKobo:       rem.AmountKobo,
		CounterpartyName: rem.RecipientName,
		Provider:         s.screenerName,
		ProviderRef:      providerRef,
		Decision:         decision,
		MatchedNames:     matched,
		ListVersion:      listVersion,
		Message:          message,
	})
	if err != nil {
		return "", err
	}
	return recorded.Decision, nil
}

// buildReceipt resolves the agent and region names and renders the receipt text.
func (s *AgentRemittanceService) buildReceipt(ctx context.Context, res store.RedeemPickupResult) (RedeemReceipt, error) {
	receipt := RedeemReceipt{Remittance: res.Remittance, Payout: res.Payout, AmountText: domain.FormatNGN(res.Remittance.AmountKobo)}
	if merchant, err := s.store.MerchantByID(ctx, res.Remittance.AgentMerchantID); err == nil {
		receipt.AgentName = merchant.Name
	}
	if region, err := s.store.AgentRegionByID(ctx, res.Remittance.RegionID); err == nil {
		receipt.RegionName = region.Name
	}
	receipt.ReceiptText = fmt.Sprintf(
		"*Cash pickup receipt*\n\nPaid to: %s\nAmount: %s\nAgent: %s\nLocation: %s\nID sighted: %s\n\nKeep this receipt. Reference: %s",
		res.Remittance.RecipientName, receipt.AmountText, nonEmpty(receipt.AgentName, "Agent"),
		nonEmpty(receipt.RegionName, "—"), res.Payout.ReceiverIDType, res.Payout.ID.String())
	return receipt, nil
}

func nonEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
