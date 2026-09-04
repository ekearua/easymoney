package service

import "whatsapp-payment-demo/internal/config"

// FeeResult is a computed fee for one payment leg.
type FeeResult struct {
	Channel    string
	AmountKobo int64
	FeeKobo    int64
}

// XegoCollectionFee computes the platform fee for a merchant collection
// payment. It looks up the channel-based parameters from the config and
// applies: fee = min(bps × amount / 10000 + fixed, cap). Returns zero
// fee for unknown channels (bank_transfer simulated rail has no collection
// fee at this stage; the fee model is provider-agnostic).
func XegoCollectionFee(cfg config.Config, channel string, amountKobo int64) FeeResult {
	if amountKobo <= 0 {
		return FeeResult{Channel: channel, AmountKobo: amountKobo}
	}
	var bps, fixed, cap int64
	switch channel {
	case "card":
		bps = cfg.FeeCardBPS
		fixed = cfg.FeeCardFixedKobo
		cap = cfg.FeeCardCapKobo
	case "dva", "bank_transfer":
		bps = cfg.FeeDVABPS
		fixed = cfg.FeeDVAFixedKobo
		cap = cfg.FeeDVACapKobo
	case "transfer":
		bps = cfg.FeeTransferBPS
		fixed = cfg.FeeTransferFixedKobo
		cap = cfg.FeeTransferCapKobo
	default:
		return FeeResult{Channel: channel, AmountKobo: amountKobo}
	}
	fee := amountKobo*bps/10_000 + fixed
	if cap > 0 && fee > cap {
		fee = cap
	}
	if fee > amountKobo {
		fee = amountKobo
	}
	return FeeResult{Channel: channel, AmountKobo: amountKobo, FeeKobo: fee}
}

// FeeChannelForProvider maps a payment rail/provider to the fee channel used
// to look up collection-fee parameters. Card rails (interswitch) bill under
// "card"; the simulated in-app bank-transfer rail bills under "dva"; wallet
// payments are instant like a card checkout and bill under the same "card"
// parameters.
func FeeChannelForProvider(provider string) string {
	switch provider {
	case ProviderInterswitch:
		return "card"
	case ProviderBankTransfer:
		return "dva"
	case ProviderWallet:
		return "card"
	default:
		return provider
	}
}

// XegoPayoutFee returns the flat NIP payout fee deducted from the payout
// amount. The recipient receives (amount − fee).
func XegoPayoutFee(cfg config.Config, amountKobo int64) int64 {
	fee := cfg.FeeNIPPayoutFlatKobo
	if fee > amountKobo {
		return amountKobo
	}
	return fee
}

// MerchantReceivable returns the merchant's share after deducting the
// collection fee from the collected amount.
func MerchantReceivable(amountKobo, feeKobo int64) int64 {
	recv := amountKobo - feeKobo
	if recv < 0 {
		return 0
	}
	return recv
}
