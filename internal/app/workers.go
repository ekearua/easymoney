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
	"whatsapp-payment-demo/internal/store"
)

func (a *App) runWorkers(ctx context.Context) {
	outboxTicker := time.NewTicker(2 * time.Second)
	eventTicker := time.NewTicker(1 * time.Second)
	reconcileTicker := time.NewTicker(1 * time.Minute)
	recon3Ticker := time.NewTicker(24 * time.Hour)
	retentionTicker := time.NewTicker(24 * time.Hour)
	rescreenTicker := time.NewTicker(24 * time.Hour)
	auditChainTicker := time.NewTicker(a.cfg.AuditChainVerifyInterval)
	monitorTicker := time.NewTicker(15 * time.Minute)
	fraudTicker := time.NewTicker(15 * time.Minute)
	settlementTicker := time.NewTicker(10 * time.Second)
	onboardReminderTicker := time.NewTicker(a.cfg.OnboardingReminderInterval)
	bulkTicker := time.NewTicker(2 * time.Second)
	purseExpiryTicker := time.NewTicker(6 * time.Hour)
	defer outboxTicker.Stop()
	defer eventTicker.Stop()
	defer reconcileTicker.Stop()
	defer recon3Ticker.Stop()
	defer retentionTicker.Stop()
	defer rescreenTicker.Stop()
	defer auditChainTicker.Stop()
	defer monitorTicker.Stop()
	defer fraudTicker.Stop()
	defer settlementTicker.Stop()
	defer onboardReminderTicker.Stop()
	defer bulkTicker.Stop()
	defer purseExpiryTicker.Stop()
	a.startEventConsumers(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-outboxTicker.C:
			// Each pipeline stage dispatches onto its own gate, so a slow
			// provider call or a big inbound batch no longer stalls the rest
			// of the cycle the way the old inline loop did. Every class stays
			// bounded by its WORKER_*_CONCURRENCY pool; a saturated or
			// still-running handler drops this tick instead of queueing it.
			// Inbound and outbox carry a nil class because they fan their
			// claimed batch out internally.
			a.dispatch(ctx, nil, "inbound", a.processInboundMessages)
			a.dispatch(ctx, a.workerPools.webhook, "gateway_webhooks", a.processGatewayWebhooks)
			a.dispatch(ctx, a.workerPools.webhook, "vtpass_webhooks", a.processVTPassWebhooks)
			a.dispatch(ctx, a.workerPools.webhook, "merchant_webhooks", a.processMerchantWebhooks)
			a.dispatch(ctx, a.workerPools.data, "data_fulfilments", a.processDataFulfilments)
			a.dispatch(ctx, nil, "outbox", a.deliverOutbox)
			a.dispatch(ctx, a.workerPools.misc, "expire_checkouts", a.expireCheckouts)
			a.dispatch(ctx, a.workerPools.misc, "pending_payment_hooks", a.processPendingPaymentHooks)
		case <-eventTicker.C:
			a.dispatch(ctx, a.workerPools.misc, "event_drain", func(ctx context.Context) {
				if err := a.publisher.Drain(ctx); err != nil {
					a.logger.WarnContext(ctx, "event publisher failed", "error", err)
				}
			})
		case <-reconcileTicker.C:
			a.dispatch(ctx, a.workerPools.recon, "reconcile", func(ctx context.Context) {
				if err := a.payments.Reconcile(ctx); err != nil {
					a.logger.WarnContext(ctx, "scheduled reconciliation failed", "error", err)
				}
			})
		case <-recon3Ticker.C:
			a.dispatch(ctx, a.workerPools.recon, "recon3", func(ctx context.Context) {
				if _, err := a.runReconciliationAuto(ctx); err != nil {
					a.logger.WarnContext(ctx, "scheduled three-way reconciliation failed", "error", err)
				}
			})
		case <-retentionTicker.C:
			a.dispatch(ctx, a.workerPools.recon, "retention", func(ctx context.Context) {
				if err := a.PurgeExpiredData(ctx); err != nil {
					a.logger.WarnContext(ctx, "scheduled retention failed", "error", err)
				}
			})
		case <-rescreenTicker.C:
			a.dispatch(ctx, a.workerPools.misc, "rescreen", func(ctx context.Context) {
				if err := a.RescreenDue(ctx); err != nil {
					a.logger.WarnContext(ctx, "scheduled KYC rescreen failed", "error", err)
				}
			})
		case <-auditChainTicker.C:
			a.dispatch(ctx, a.workerPools.misc, "audit_chain", func(ctx context.Context) {
				if err := a.RunAuditChainVerification(ctx); err != nil {
					a.logger.WarnContext(ctx, "scheduled audit chain verification failed", "error", err)
				}
			})
		case <-monitorTicker.C:
			a.dispatch(ctx, a.workerPools.misc, "monitor", func(ctx context.Context) {
				if err := a.MonitorTransactions(ctx); err != nil {
					a.logger.WarnContext(ctx, "scheduled transaction monitor failed", "error", err)
				}
			})
		case <-fraudTicker.C:
			a.dispatch(ctx, a.workerPools.misc, "fraud", func(ctx context.Context) {
				if err := a.MonitorFraud(ctx); err != nil {
					a.logger.WarnContext(ctx, "scheduled fraud monitor failed", "error", err)
				}
			})
		case <-settlementTicker.C:
			a.dispatch(ctx, a.workerPools.misc, "settlement", a.runSettlementDispatcher)
		case <-onboardReminderTicker.C:
			a.dispatch(ctx, a.workerPools.misc, "onboard_reminders", func(ctx context.Context) {
				if _, err := a.onboardReminders.RunSweep(ctx); err != nil {
					a.logger.WarnContext(ctx, "onboarding reminder sweep failed", "error", err)
				}
			})
		case <-purseExpiryTicker.C:
			a.dispatch(ctx, a.workerPools.misc, "purse_expiry", func(ctx context.Context) {
				if a.programPurses == nil {
					return
				}
				closed, returned, err := a.programPurses.RunExpirySweep(ctx)
				if err != nil {
					a.logger.WarnContext(ctx, "program purse expiry sweep failed", "error", err)
					return
				}
				if closed > 0 {
					a.logger.InfoContext(ctx, "program purses expired", "closed", closed, "returned_kobo", returned)
				}
			})
		case <-bulkTicker.C:
			a.runBulkJobs(ctx)
		}
	}
}

// runBulkJobs claims due bulk jobs and runs them on bounded workers so a burst
// of large batches cannot exhaust the tick loop.
func (a *App) runBulkJobs(ctx context.Context) {
	if a.bulkPayments == nil {
		return
	}
	limit := cap(a.bulkSlots) - len(a.bulkSlots)
	if limit <= 0 {
		return
	}
	jobs, err := a.store.ClaimBulkJobs(ctx, limit)
	if err != nil {
		a.logger.WarnContext(ctx, "claim bulk jobs", "error", err)
		return
	}
	for _, job := range jobs {
		job := job
		a.bulkSlots <- struct{}{}
		a.workerWg.Add(1)
		go func() {
			defer a.workerWg.Done()
			defer func() { <-a.bulkSlots }()
			if err := a.handleBulkJob(ctx, job); err != nil {
				a.logger.WarnContext(ctx, "bulk job failed", "kind", job.Kind, "batch", job.BatchID, "error", err)
				if retryErr := a.store.RetryBulkJob(ctx, job.ID, err.Error()); retryErr != nil {
					a.logger.WarnContext(ctx, "retry bulk job", "job", job.ID, "error", retryErr)
				}
				return
			}
			if err := a.store.CompleteBulkJob(ctx, job.ID); err != nil {
				a.logger.WarnContext(ctx, "complete bulk job", "job", job.ID, "error", err)
			}
		}()
	}
}

// handleBulkJob dispatches one claimed bulk job to the matching stage.
func (a *App) handleBulkJob(ctx context.Context, job store.BulkJob) error {
	switch job.Kind {
	case store.BulkJobStage1:
		return a.bulkPayments.RunStage1(ctx, job.BatchID)
	case store.BulkJobStage2:
		return a.bulkPayments.RunStage2(ctx, job.BatchID)
	default:
		return fmt.Errorf("unknown bulk job kind %q", job.Kind)
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

// processInboundMessages claims one batch and fans it out across the inbound
// worker pool. The batch is grouped by conversation first, so different
// conversations are handled in parallel while one conversation's messages
// stay strictly ordered — the FSM session is a load-modify-save cycle, and
// interleaving it is how a conversation gets stuck. The handler gate keeps
// one batch at a time per replica; cross-replica ordering is the
// per-recipient advisory lock taken by ClaimInboundMessages. The call stays
// synchronous — it waits for the whole batch — because the simulations and
// channel smoke tests drive it one step at a time.
func (a *App) processInboundMessages(ctx context.Context) {
	messages, err := a.store.ClaimInboundMessages(ctx, 20)
	if err != nil {
		a.logger.WarnContext(ctx, "claim inbound messages", "error", err)
		return
	}
	a.processInboundBatch(ctx, messages)
}

// processInboundBatch runs each conversation's messages in order on a bounded
// inbound slot and waits for the whole batch to finish.
func (a *App) processInboundBatch(ctx context.Context, messages []store.InboundMessage) {
	groups := groupByKey(messages, inboundGroupKey)
	runPooled(a.workerPools.inbound, groups, func(group []store.InboundMessage) {
		for _, message := range group {
			a.handleInboundMessage(ctx, message)
		}
	})
}

// inboundGroupKey identifies the conversation a message belongs to: its
// channel plus the recipient chat, falling back to the sender where a
// channel records only one identity.
func inboundGroupKey(message store.InboundMessage) string {
	if message.Recipient != "" {
		return message.Channel + "\x00" + message.Recipient
	}
	return message.Channel + "\x00" + message.Sender
}

// handleInboundMessage runs one message through the FSM (or the bulk-location
// side path) and marks it done or schedules its bounded retry.
func (a *App) handleInboundMessage(ctx context.Context, message store.InboundMessage) {
	if message.HasLocation && a.bulkPayments != nil {
		handled, locErr := a.bulkPayments.RecordLocation(ctx, message.Recipient, message.Latitude, message.Longitude, message.Accuracy)
		if locErr != nil {
			a.logger.WarnContext(ctx, "record bulk location", "message_id", message.ID, "channel", message.Channel, "error", locErr)
		} else if handled {
			_ = a.store.CompleteInboundMessage(ctx, message.ID)
			return
		}
	}
	if err := a.conversation.Handle(ctx, message); err != nil {
		a.logger.ErrorContext(ctx, "process inbound message", "message_id", message.ID, "channel", message.Channel, "error", err)
		_ = a.store.RetryInboundMessage(ctx, message.ID, message.Attempts, err.Error())
		return
	}
	_ = a.store.CompleteInboundMessage(ctx, message.ID)
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

// processPendingPaymentHooks retries post-success payment hooks (invoice,
// thrift, service, event, splits, receipt scan) that failed inline, so a
// succeeded payment is never left with its purpose un-applied.
func (a *App) processPendingPaymentHooks(ctx context.Context) {
	if err := a.payments.ApplyPendingPaymentHooks(ctx); err != nil {
		a.logger.WarnContext(ctx, "process pending payment hooks", "error", err)
	}
}

func (a *App) processDataFulfilments(ctx context.Context) {
	if err := a.data.ProcessFulfilments(ctx, 20); err != nil {
		a.logger.WarnContext(ctx, "process data fulfilments", "error", err)
	}
}

// deliverOutbox claims one batch of outbound messages and fans it out across
// the outbound worker pool, grouped by conversation so one recipient's
// messages go out in order while different recipients send in parallel. It
// waits for the whole batch, matching the synchronous step the tests drive.
func (a *App) deliverOutbox(ctx context.Context) {
	messages, err := a.store.ClaimOutbox(ctx, 20)
	if err != nil {
		a.logger.WarnContext(ctx, "claim outbox", "error", err)
		return
	}
	groups := groupByKey(messages, func(message store.OutboxMessage) string {
		return message.Channel + "\x00" + message.Recipient
	})
	runPooled(a.workerPools.outbox, groups, func(group []store.OutboxMessage) {
		for _, message := range group {
			a.deliverOutboxMessage(ctx, message)
		}
	})
}

// deliverOutboxMessage sends one outbox row and records its terminal outcome
// (complete, or bounded retry).
func (a *App) deliverOutboxMessage(ctx context.Context, message store.OutboxMessage) {
	var sendErr error
	switch message.Kind {
	case "text":
		var payload struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			sendErr = err
		} else {
			providerRef, err := a.sendOutboxText(ctx, message.Channel, message.Recipient, payload.Body)
			sendErr = err
			a.persistOutboxProviderRef(ctx, message, sendErr == nil, providerRef)
		}
	case "image":
		var payload struct {
			ImageData string `json:"image_data"`
			Caption   string `json:"caption"`
		}
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			sendErr = err
		} else {
			providerRef, err := a.sendOutboxImage(ctx, message.Channel, message.Recipient, payload.ImageData, payload.Caption)
			sendErr = err
			a.persistOutboxProviderRef(ctx, message, sendErr == nil, providerRef)
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
			providerRef, err := a.sendOutboxText(ctx, message.Channel, message.Recipient, strings.Join(payload.Parameters, "\n"))
			sendErr = err
			a.persistOutboxProviderRef(ctx, message, sendErr == nil, providerRef)
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

// persistOutboxProviderRef records the provider message identifier returned by
// a successful send so delivery-status webhooks can reconcile the row later.
func (a *App) persistOutboxProviderRef(ctx context.Context, message store.OutboxMessage, sent bool, providerRef string) {
	if sent && providerRef != "" {
		if err := a.store.SetOutboxProviderRef(ctx, message.ID, providerRef); err != nil {
			a.logger.WarnContext(ctx, "set outbox provider ref", "id", message.ID, "error", err)
		}
	}
}

func (a *App) sendOutboxText(ctx context.Context, channel, recipient, body string) (string, error) {
	switch channel {
	case service.ChannelSMS:
		// Webhook mode (SMS_PROVIDER unset/webhook) never enqueues SMS rows,
		// so a nil sender is a safe no-op. With SMS_PROVIDER=http the sender
		// delivers outbound SMS and returns the provider message identifier
		// for lifecycle tracking; the SMS gateway then reports delivery
		// status to /webhooks/sms/status.
		if a.smsSender == nil {
			return "", nil
		}
		return a.smsSender.Send(ctx, recipient, body)
	case service.ChannelTelegram:
		if a.telegram == nil {
			return "", errors.New("Telegram is not configured")
		}
		return "", a.telegram.SendText(ctx, recipient, body)
	case service.ChannelInstagram:
		if a.instagram == nil {
			return "", errors.New("Instagram is not configured")
		}
		return "", a.instagram.SendText(ctx, recipient, body)
	case service.ChannelTikTok:
		if a.tiktok == nil {
			return "", errors.New("TikTok is not configured")
		}
		return "", a.tiktok.SendText(ctx, recipient, body)
	default:
		return "", a.whatsapp.SendText(ctx, recipient, body)
	}
}

func (a *App) sendOutboxImage(ctx context.Context, channel, recipient, imageDataB64, caption string) (string, error) {
	imageData, err := base64.StdEncoding.DecodeString(imageDataB64)
	if err != nil {
		return "", fmt.Errorf("decode image data: %w", err)
	}
	switch channel {
	case service.ChannelSMS:
		// SMS has no image support; deliver a text notice instead.
		if a.smsSender == nil {
			return "", nil
		}
		text := caption
		if text == "" {
			text = "Receipt/document sent. Check the app."
		}
		return a.smsSender.Send(ctx, recipient, text)
	case service.ChannelTelegram:
		if a.telegram == nil {
			return "", errors.New("Telegram is not configured")
		}
		return "", a.telegram.SendImage(ctx, recipient, imageData, caption)
	case service.ChannelInstagram:
		if a.instagram == nil {
			return "", errors.New("Instagram is not configured")
		}
		return "", a.instagram.SendImage(ctx, recipient, imageData, caption)
	case service.ChannelTikTok:
		if a.tiktok == nil {
			return "", errors.New("TikTok is not configured")
		}
		return "", a.tiktok.SendImage(ctx, recipient, imageData, caption)
	default:
		return "", a.whatsapp.SendImage(ctx, recipient, imageData, caption)
	}
}
