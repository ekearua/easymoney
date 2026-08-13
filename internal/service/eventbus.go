package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/kyc"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// EventPublisher drains the transactional business-event outbox onto the
// event bus. It is the write side of the Phase 3 backbone: producers write
// facts into business_event_outbox inside their own transaction, and the
// publisher delivers them to every subscribed consumer group exactly-once per
// successful publish (at-least-once overall, so consumers must be idempotent).
type EventPublisher struct {
	store  *store.Store
	bus    ports.EventBus
	logger *slog.Logger
}

// NewEventPublisher constructs the outbox->bus publisher.
func NewEventPublisher(repository *store.Store, bus ports.EventBus, logger *slog.Logger) *EventPublisher {
	return &EventPublisher{store: repository, bus: bus, logger: logger}
}

// Drain publishes one bounded batch of outboxed events. Call on a ticker.
func (p *EventPublisher) Drain(ctx context.Context) error {
	events, err := p.store.ClaimBusinessEvents(ctx, 50)
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := p.bus.Publish(ctx, ports.EventMessage{Topic: event.Topic, Key: event.Key, Payload: event.Payload}); err != nil {
			p.logger.Error("publish business event failed", "event_id", event.ID, "topic", event.Topic, "error", err)
			_ = p.store.RetryBusinessEvent(ctx, event.ID, event.Attempts, err.Error())
			continue
		}
		if err := p.store.CompleteBusinessEvent(ctx, event.ID); err != nil {
			p.logger.Error("complete business event failed", "event_id", event.ID, "error", err)
		}
	}
	return nil
}

// NotificationConsumer sends payment notifications to merchants. It replaces
// the inline notifyMerchantPayment calls that used to run inside the payment
// transaction; the customer notification remains on the synchronous path.
type NotificationConsumer struct {
	store    *store.Store
	payments *PaymentService
	logger   *slog.Logger
}

// NewNotificationConsumer constructs the notification event consumer.
func NewNotificationConsumer(repository *store.Store, payments *PaymentService, logger *slog.Logger) *NotificationConsumer {
	return &NotificationConsumer{store: repository, payments: payments, logger: logger}
}

// HandlePaymentSucceeded is the payment.succeeded handler. It claims the
// payment's merchant-notification slot atomically so redelivery does not
// double-notify.
func (c *NotificationConsumer) HandlePaymentSucceeded(ctx context.Context, msg ports.EventMessage) error {
	var event domain.PaymentSucceeded
	if err := json.Unmarshal(msg.Payload, &event); err != nil {
		return fmt.Errorf("decode payment.succeeded: %w", err)
	}
	paymentID, err := uuid.Parse(event.PaymentID)
	if err != nil {
		return fmt.Errorf("parse payment id: %w", err)
	}
	claimed, err := c.store.ClaimMerchantNotification(ctx, paymentID)
	if err != nil {
		return err
	}
	if !claimed {
		c.logger.Info("merchant notification already sent", "payment_id", paymentID)
		return nil
	}
	payment, err := c.store.PaymentByID(ctx, paymentID)
	if err != nil {
		return err
	}
	c.payments.NotifyMerchantPayment(ctx, payment)
	return nil
}

// ComplianceConsumer runs per-payment transaction monitoring as an event
// consumer. Each settled payment is evaluated against the monitoring rules
// together with the customer's recent history; alerts flow into the compliance
// queue exactly as the periodic full-scan monitor does.
type ComplianceConsumer struct {
	store  *store.Store
	cfg    kyc.MonitorConfig
	logger *slog.Logger
}

// NewComplianceConsumer constructs the compliance event consumer.
func NewComplianceConsumer(repository *store.Store, cfg kyc.MonitorConfig, logger *slog.Logger) *ComplianceConsumer {
	return &ComplianceConsumer{store: repository, cfg: cfg, logger: logger}
}

// HandlePaymentSucceeded is the payment.succeeded handler. It is idempotent
// via the store's existing-alert check.
func (c *ComplianceConsumer) HandlePaymentSucceeded(ctx context.Context, msg ports.EventMessage) error {
	var event domain.PaymentSucceeded
	if err := json.Unmarshal(msg.Payload, &event); err != nil {
		return fmt.Errorf("decode payment.succeeded: %w", err)
	}
	paymentID, err := uuid.Parse(event.PaymentID)
	if err != nil {
		return fmt.Errorf("parse payment id: %w", err)
	}
	input, err := c.store.PaymentTransactionInput(ctx, paymentID)
	if err != nil {
		return err
	}
	raised, err := c.store.RunTransactionMonitorForPayment(ctx, input, c.cfg)
	if err != nil {
		return err
	}
	if raised > 0 {
		c.logger.Info("event-driven monitoring raised alerts", "payment_id", paymentID, "alerts", raised)
	}
	return nil
}
