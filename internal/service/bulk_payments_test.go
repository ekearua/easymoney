package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/ports"
)

type recordingMessenger struct {
	texts     []string
	locations []string
	links     []string
	templates []string
	locErr    error
}

func (m *recordingMessenger) SendText(_ context.Context, _, body string) error {
	m.texts = append(m.texts, body)
	return nil
}
func (m *recordingMessenger) SendInteractive(context.Context, ports.InteractiveMessage) error {
	return nil
}
func (m *recordingMessenger) SendCheckout(_ context.Context, _, _, _ string) error { return nil }
func (m *recordingMessenger) SendLink(_ context.Context, _, _, url, _ string) error {
	m.links = append(m.links, url)
	return nil
}
func (m *recordingMessenger) SendTemplate(_ context.Context, _, _ string, _ []string) error {
	return nil
}
func (m *recordingMessenger) SendImage(_ context.Context, _ string, _ []byte, _ string) error {
	return nil
}
func (m *recordingMessenger) SendLocationRequest(_ context.Context, _, body string) error {
	if m.locErr != nil {
		return m.locErr
	}
	m.locations = append(m.locations, body)
	return nil
}

var errNativeLocation = errors.New("native location unavailable")

func TestDeliverStage1RegisteredUsesNativeRequest(t *testing.T) {
	messenger := &recordingMessenger{}
	svc := &BulkPaymentsService{cfg: BulkPaymentsConfig{SendMessage: "share", InviteMessage: "invite", BaseURL: "https://xego.test"}}
	if err := svc.deliverStage1(context.Background(), messenger, ChannelWhatsApp, uuid.New(), "+2348011111111", true); err != nil {
		t.Fatalf("deliverStage1: %v", err)
	}
	if len(messenger.locations) != 1 {
		t.Fatalf("want native location request, got %d", len(messenger.locations))
	}
}

func TestDeliverStage1FallsBackToLink(t *testing.T) {
	messenger := &recordingMessenger{}
	svc := &BulkPaymentsService{cfg: BulkPaymentsConfig{SendMessage: "share", InviteMessage: "invite", BaseURL: "https://xego.test"}}
	itemID := uuid.New()
	messenger.locErr = errNativeLocation
	if err := svc.deliverStage1(context.Background(), messenger, ChannelWhatsApp, itemID, "+2348011111111", true); err != nil {
		t.Fatalf("deliverStage1: %v", err)
	}
	if len(messenger.links) != 1 {
		t.Fatalf("want one fallback link, got %d", len(messenger.links))
	}
	if want := "https://xego.test/bulk/loc?id=" + itemID.String(); messenger.links[0] != want {
		t.Fatalf("link = %q, want %q", messenger.links[0], want)
	}
}

func TestDeliverStage1UnregisteredInvites(t *testing.T) {
	messenger := &recordingMessenger{}
	svc := &BulkPaymentsService{cfg: BulkPaymentsConfig{SendMessage: "share", InviteMessage: "invite"}}
	if err := svc.deliverStage1(context.Background(), messenger, ChannelWhatsApp, uuid.New(), "+2348011111111", false); err != nil {
		t.Fatalf("deliverStage1: %v", err)
	}
	if len(messenger.texts) != 1 || messenger.texts[0] != "invite" {
		t.Fatalf("want invite text, got %+v", messenger.texts)
	}
}

func TestLocationURLDefault(t *testing.T) {
	svc := &BulkPaymentsService{cfg: BulkPaymentsConfig{BaseURL: "https://xego.test"}}
	if got := svc.LocationURL(); got != "https://xego.test/bulk/loc" {
		t.Fatalf("LocationURL = %q", got)
	}
}
