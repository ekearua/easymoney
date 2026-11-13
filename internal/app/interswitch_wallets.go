package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	interswitchprovider "whatsapp-payment-demo/internal/providers/interswitch"
	"whatsapp-payment-demo/internal/store"
)

func (a *App) provisionInterswitchWallet(ctx context.Context, user store.User, displayName, email, mobile, pin string) error {
	if a.interswitch == nil || !a.cfg.InterswitchWalletsEnabled {
		return errors.New("Interswitch wallet provisioning is not enabled")
	}
	account, err := a.store.StandingAccountByUser(ctx, user.ID)
	if err == nil && account.ProvisioningStatus == "provisioned" {
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load wallet provisioning state: %w", err)
	}
	if err == nil && account.ProvisioningStatus != "not_started" {
		return errors.New("provider wallet creation is already pending or needs operator review")
	}
	if strings.TrimSpace(a.cfg.InterswitchMerchantCode) == "" {
		return errors.New("Interswitch merchant code is not configured")
	}
	if !user.EmailVerifiedAt.Valid || strings.TrimSpace(email) == "" {
		return errors.New("verified email is required to create an Interswitch wallet")
	}
	firstName := strings.Fields(strings.TrimSpace(displayName))
	if len(firstName) == 0 {
		return errors.New("customer name is required to create an Interswitch wallet")
	}
	mobile = normalizeWalletMobile(mobile)
	if !validWalletMobile(mobile) {
		return errors.New("valid mobile number is required to create an Interswitch wallet")
	}
	if !isFourDigitPIN(pin) {
		return errors.New("wallet PIN must contain four digits")
	}

	if errors.Is(err, pgx.ErrNoRows) {
		account, err = a.store.EnsureStandingAccount(ctx, user.ID)
		if err != nil {
			return fmt.Errorf("reserve wallet onboarding record: %w", err)
		}
	}
	if account.ProvisioningStatus == "provisioned" {
		return nil
	}
	if account.ProvisioningStatus != "not_started" {
		return errors.New("provider wallet creation is already pending or needs operator review")
	}
	claimed, err := a.store.ClaimStandingWalletProvisioning(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("claim provider wallet provisioning: %w", err)
	}
	if !claimed {
		return errors.New("provider wallet creation is already pending or needs operator review")
	}

	wallet, err := a.interswitch.CreateMerchantWallet(ctx, interswitchWalletCreateRequest(user, displayName, email, mobile, pin, a.cfg.InterswitchMerchantCode))
	if err != nil {
		_ = a.store.MarkStandingWalletProvisioningUnknown(ctx, user.ID, err.Error())
		return fmt.Errorf("create Interswitch wallet: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(wallet.WalletCreationStatus), "COMPLETED") ||
		!strings.EqualFold(strings.TrimSpace(wallet.Status), "ACTIVE") ||
		strings.TrimSpace(wallet.VirtualAccount.AccountNumber) == "" {
		_ = a.store.MarkStandingWalletProvisioningUnknown(ctx, user.ID, "provider response did not confirm an active wallet and virtual account")
		return errors.New("Interswitch did not confirm an active wallet and virtual account")
	}
	if err := a.store.CompleteStandingWalletProvisioning(ctx, user.ID, store.StandingWalletProvisioning{
		WalletID:             wallet.WalletID,
		ProviderWalletRecordID: wallet.ID,
		AccountPayableCode:   wallet.VirtualAccount.AccountPayableCode,
		WalletStatus:         wallet.Status,
		WalletCreationStatus: wallet.WalletCreationStatus,
		AccountNumber:        wallet.VirtualAccount.AccountNumber,
		AccountName:          wallet.VirtualAccount.AccountName,
		BankName:             wallet.VirtualAccount.BankName,
		BankCode:             wallet.VirtualAccount.BankCode,
		CurrencyCode:         wallet.VirtualAccount.CurrencyCode,
	}); err != nil {
		_ = a.store.MarkStandingWalletProvisioningUnknown(ctx, user.ID, err.Error())
		return fmt.Errorf("persist Interswitch wallet: %w", err)
	}
	return nil
}

func interswitchWalletCreateRequest(user store.User, displayName, email, mobile, pin, merchantCode string) interswitchprovider.MerchantWalletCreateRequest {
	return interswitchprovider.MerchantWalletCreateRequest{
		Name:         "Faya-" + user.ID.String(),
		MerchantCode: merchantCode,
		Status:       "ACTIVE",
		MobileNumber: mobile,
		Provider:     "PRIME",
		FirstName:    strings.Fields(strings.TrimSpace(displayName))[0],
		PIN:          pin,
		Email:        strings.TrimSpace(email),
	}
}

func isFourDigitPIN(pin string) bool {
	if len(pin) != 4 {
		return false
	}
	for _, digit := range pin {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func normalizeWalletMobile(mobile string) string {
	return strings.NewReplacer("+", "", " ", "", "-", "", "(", "", ")", "").Replace(strings.TrimSpace(mobile))
}

func validWalletMobile(mobile string) bool {
	if len(mobile) < 10 || len(mobile) > 15 {
		return false
	}
	for _, digit := range mobile {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
