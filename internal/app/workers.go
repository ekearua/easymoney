package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/service"
)

func (a *App) runWorkers(ctx context.Context) {
	outboxTicker := time.NewTicker(2 * time.Second)
	eventTicker := time.NewTicker(1 * time.Second)
	reconcileTicker := time.NewTicker(1 * time.Minute)
	recon3Ticker := time.NewTicker(24 * time.Hour)
	retentionTicker := time.NewTicker(24 * time.Hour)
	rescreenTicker := time.NewTicker(24 * time.Hour)
	monitorTicker := time.NewTicker(15 * time.Minute)
	settlementTicker := time.NewTicker(10 * time.Second)
	defer outboxTicker.Stop()
	defer eventTicker.Stop()
	defer reconcileTicker.Stop()
	defer recon3Ticker.Stop()
	defer retentionTicker.Stop()
	defer rescreenTicker.Stop()
	defer monitorTicker.Stop()
	defer settlementTicker.Stop()
	a.startEventConsumers(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-outboxTicker.C:
			a.workerWg.Add(1)
			a.processInboundMessages(ctx)
			a.processGatewayWebhooks(ctx)
			a.processVTPassWebhooks(ctx)
			a.processDataFulfilments(ctx)
			a.deliverOutbox(ctx)
			a.processMerchantWebhooks(ctx)
			a.expireCheckouts(ctx)
			a.workerWg.Done()
		case <-eventTicker.C:
			a.workerWg.Add(1)
			if err := a.publisher.Drain(ctx); err != nil {
				a.logger.WarnContext(ctx, "event publisher failed", "error", err)
			}
			a.workerWg.Done()
		case <-reconcileTicker.C:
			a.workerWg.Add(1)
			if err := a.payments.Reconcile(ctx); err != nil {
				a.logger.WarnContext(ctx, "scheduled reconciliation failed", "error", err)
			}
			a.workerWg.Done()
		case <-recon3Ticker.C:
			a.workerWg.Add(1)
			if _, err := a.runReconciliationAuto(ctx); err != nil {
				a.logger.WarnContext(ctx, "scheduled three-way reconciliation failed", "error", err)
			}
			a.workerWg.Done()
		case <-retentionTicker.C:
			a.workerWg.Add(1)
			if err := a.PurgeExpiredData(ctx); err != nil {
				a.logger.WarnContext(ctx, "scheduled retention failed", "error", err)
			}
			a.workerWg.Done()
		case <-rescreenTicker.C:
			a.workerWg.Add(1)
			if err := a.RescreenDue(ctx); err != nil {
				a.logger.WarnContext(ctx, "scheduled KYC rescreen failed", "error", err)
			}
			a.workerWg.Done()
		case <-monitorTicker.C:
			a.workerWg.Add(1)
			if err := a.MonitorTransactions(ctx); err != nil {
				a.logger.WarnContext(ctx, "scheduled transaction monitor failed", "error", err)
			}
			a.workerWg.Done()
		case <-settlementTicker.C:
			a.workerWg.Add(1)
			a.runSettlementDispatcher(ctx)
			a.workerWg.Done()
		}
	}
}

func (a *App) startEventConsumers(ctx context.Context) {
	notification := service.NewNotificationConsumer(a.store, a.payments, a.logger)
	compliance := service.NewComplianceConsumer(a.store, a.monitorConfig(), a.logger)
	webhooks := service.NewMerchantWebhookConsumer(a.store, a.cfg.BaseURL, a.logger)
	for _, sub := range []struct {
		topic   string
		group   string
		handler ports.EventHandler
	}{
		{domain.TopicPaymentSucceeded, "xego.notifications", notification.HandlePaymentSucceeded},
		{domain.TopicPaymentSucceeded, "xego.compliance", compliance.HandlePaymentSucceeded},
		{domain.TopicPaymentSucceeded, "xego.merchant_webhooks", webhooks.HandlePaymentSucceeded},
		{domain.TopicPaymentFailed, "xego.merchant_webhooks", webhooks.HandlePaymentFailed},
		{domain.TopicSettlementBatchCreated, "xego.merchant_webhooks", webhooks.HandleSettlementBatchCreated},
		{domain.TopicSettlementBatchProcessed, "xego.merchant_webhooks", webhooks.HandleSettlementBatchProcessed},
		{domain.TopicPayoutSucceeded, "xego.merchant_webhooks", webhooks.HandlePayoutSucceeded},
		{domain.TopicPayoutFailed, "xego.merchant_webhooks", webhooks.HandlePayoutFailed},
		{domain.TopicPaymentRefunded, "xego.merchant_webhooks", webhooks.HandlePaymentRefunded},
		{domain.TopicPaymentDisputed, "xego.merchant_webhooks", webhooks.HandlePaymentDisputed},
	} {
		if err := a.eventBus.Subscribe(ctx, sub.topic, sub.group, sub.handler); err != nil {
			a.logger.ErrorContext(ctx, "subscribe event consumer failed", "topic", sub.topic, "group", sub.group, "error", err)
		}
	}
}

func (a *App) processInboundMessages(ctx context.Context) {
	messages, err := a.store.ClaimInboundMessages(ctx, 20)
	if err != nil {
		a.logger.WarnContext(ctx, "claim inbound messages", "error", err)
		return
	}
	for _, message := range messages {
		if err := a.conversation.Handle(ctx, message); err != nil {
			a.logger.ErrorContext(ctx, "process inbound message", "message_id", message.ID, "channel", message.Channel, "error", err)
			_ = a.store.RetryInboundMessage(ctx, message.ID, message.Attempts, err.Error())
			continue
		}
		_ = a.store.CompleteInboundMessage(ctx, message.ID)
	}
}

func (a *App) processGatewayWebhooks(ctx context.Context) {
	for _, provider := range a.payments.ProviderList() {
		events, err := a.store.ClaimGatewayWebhooks(ctx, provider, 20)
		if err != nil {
			a.logger.WarnContext(ctx, "claim gateway webhooks", "provider", provider, "error", err)
			continue
		}
		for _, event := range events {
			if !a.payments.IsGatewaySuccessEvent(provider, event.Event) {
				_ = a.store.CompleteWebhook(ctx, event.ID, "ignored", "")
				continue
			}
			_, _, processErr := a.payments.VerifyAndApply(ctx, event.Reference, provider+".webhook")
			if processErr != nil {
				a.logger.ErrorContext(ctx, "process gateway webhook", "reference", event.Reference, "provider", provider, "error", processErr)
				_ = a.store.RetryWebhook(ctx, event.ID, event.Attempts, processErr.Error())
				continue
			}
			_ = a.store.CompleteWebhook(ctx, event.ID, "processed", "")
		}
	}
}

// processVTPassWebhooks claims pending VTPass webhooks and applies the results.
func (a *App) processVTPassWebhooks(ctx context.Context) {
	events, err := a.store.ClaimGatewayWebhooks(ctx, "vtpass", 20)
	if err != nil {
		a.logger.WarnContext(ctx, "claim VTPass webhooks", "error", err)
		return
	}
	for _, event := range events {
		_, _, processErr := a.data.ApplyProviderResult(ctx, event.Reference, event.Event, event.Message)
		if processErr != nil {
			a.logger.ErrorContext(ctx, "process VTPass webhook", "reference", event.Reference, "error", processErr)
			_ = a.store.RetryWebhook(ctx, event.ID, event.Attempts, processErr.Error())
			continue
		}
		_ = a.store.CompleteWebhook(ctx, event.ID, "processed", "")
	}
}

// processMerchantWebhooks delivers one batch of signed outbound webhooks to
// merchant callback URLs.
func (a *App) processMerchantWebhooks(ctx context.Context) {
	if err := a.merchantWebhooks.DeliverDue(ctx); err != nil {
		a.logger.WarnContext(ctx, "process merchant webhooks", "error", err)
	}
}

// expireCheckouts closes request-money links that passed their expiry.
func (a *App) expireCheckouts(ctx context.Context) {
	if err := a.store.ExpireCheckouts(ctx, time.Now()); err != nil {
		a.logger.WarnContext(ctx, "expire checkouts", "error", err)
	}
}

func (a *App) processDataFulfilments(ctx context.Context) {
	if err := a.data.ProcessFulfilments(ctx, 20); err != nil {
		a.logger.WarnContext(ctx, "process data fulfilments", "error", err)
	}
}

func (a *App) deliverOutbox(ctx context.Context) {
	messages, err := a.store.ClaimOutbox(ctx, 20)
	if err != nil {
		a.logger.WarnContext(ctx, "claim outbox", "error", err)
		return
	}
	for _, message := range messages {
		var sendErr error
		switch message.Kind {
		case "text":
			var payload struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal(message.Payload, &payload); err != nil {
				sendErr = err
			} else {
				sendErr = a.sendOutboxText(ctx, message.Channel, message.Recipient, payload.Body)
			}
		case "image":
			var payload struct {
				ImageData string `json:"image_data"`
				Caption   string `json:"caption"`
			}
			if err := json.Unmarshal(message.Payload, &payload); err != nil {
				sendErr = err
			} else {
				sendErr = a.sendOutboxImage(ctx, message.Channel, message.Recipient, payload.ImageData, payload.Caption)
			}
		case "template":
			var payload struct {
				Name       string   `json:"name"`
				Parameters []string `json:"parameters"`
			}
			if err := json.Unmarshal(message.Payload, &payload); err != nil {
				sendErr = err
			} else if message.Channel == service.ChannelWhatsApp {
				sendErr = a.whatsapp.SendTemplate(ctx, message.Recipient, payload.Name, payload.Parameters)
			} else {
				sendErr = a.sendOutboxText(ctx, message.Channel, message.Recipient, strings.Join(payload.Parameters, "\n"))
			}
		default:
			sendErr = fmt.Errorf("unsupported outbox kind %q", message.Kind)
		}
		if sendErr == nil {
			_ = a.store.CompleteOutbox(ctx, message.ID)
		} else {
			_ = a.store.RetryOutbox(ctx, message.ID, message.Attempts, sendErr.Error())
		}
	}
}

func (a *App) sendOutboxText(ctx context.Context, channel, recipient, body string) error {
	switch channel {
	case service.ChannelSMS:
		// The SMS MVP returns replies synchronously from /webhooks/sms. A live SMS
		// sender can be wired here later without changing order fulfilment logic.
		return nil
	case service.ChannelTelegram:
		if a.telegram == nil {
			return errors.New("Telegram is not configured")
		}
		return a.telegram.SendText(ctx, recipient, body)
	default:
		return a.whatsapp.SendText(ctx, recipient, body)
	}
}

func (a *App) sendOutboxImage(ctx context.Context, channel, recipient, imageDataB64, caption string) error {
	imageData, err := base64.StdEncoding.DecodeString(imageDataB64)
	if err != nil {
		return fmt.Errorf("decode image data: %w", err)
	}
	switch channel {
	case service.ChannelSMS:
		return nil
	case service.ChannelTelegram:
		if a.telegram == nil {
			return errors.New("Telegram is not configured")
		}
		return a.telegram.SendImage(ctx, recipient, imageData, caption)
	default:
		return a.whatsapp.SendImage(ctx, recipient, imageData, caption)
	}
}
