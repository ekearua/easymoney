package service

import (
	"context"
	"strings"
	"testing"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

// newChannelTestService builds a ConversationService whose messengers map
// covers every chat channel so Handle can be driven per channel without a
// database (the nil store only works for the paths the test touches).
func newChannelTestService(messenger ports.Messenger) *ConversationService {
	cfg := config.Config{WebFlowsEnabled: false}
	return NewConversationService(cfg, nil, nil, nil, map[string]ports.Messenger{
		ChannelWhatsApp:  messenger,
		ChannelTelegram:  messenger,
		ChannelInstagram: messenger,
		ChannelTikTok:    messenger,
	}, nil, nil, nil)
}

func TestNormalizeChannelIncludesNewChannels(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{"instagram", ChannelInstagram},
		{"INSTAGRAM", ChannelInstagram},
		{"tiktok", ChannelTikTok},
		{"TikTok", ChannelTikTok},
		{"telegram", ChannelTelegram},
		{"whatsapp", ChannelWhatsApp},
		{"unknown", ChannelWhatsApp}, // legacy default preserved
	} {
		if got := normalizeChannel(tc.in); got != tc.want {
			t.Fatalf("normalizeChannel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestOnboardingCompleteForChannelCoversNewChannels(t *testing.T) {
	t.Parallel()
	svc := newChannelTestService(&testMessenger{})
	base := store.User{OnboardingComplete: true}
	if svc.onboardingCompleteForChannel(base, ChannelWhatsApp) {
		t.Fatal("unconfirmed WhatsApp user should not be complete")
	}
	if svc.onboardingCompleteForChannel(base, ChannelInstagram) {
		t.Fatal("unconfirmed Instagram user should not be complete")
	}
	if svc.onboardingCompleteForChannel(base, ChannelTikTok) {
		t.Fatal("unconfirmed TikTok user should not be complete")
	}
	if svc.onboardingCompleteForChannel(base, ChannelTelegram) {
		t.Fatal("unconfirmed Telegram user should not be complete")
	}
}

func TestHandleAccountConfirmationLabelsPerChannel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ channel, label string }{
		{ChannelWhatsApp, "WhatsApp number"},
		{ChannelTelegram, "Telegram account"},
		{ChannelInstagram, "Instagram account"},
		{ChannelTikTok, "TikTok account"},
	} {
		messenger := &captureInteractive{}
		svc := newChannelTestService(messenger)
		if err := svc.sendAccountConfirmation(context.Background(), tc.channel, "recipient-1"); err != nil {
			t.Fatalf("%s: %v", tc.channel, err)
		}
		if !strings.Contains(messenger.lastBody, tc.label) {
			t.Fatalf("%s: expected label %q in %q", tc.channel, tc.label, messenger.lastBody)
		}
	}
}

// captureInteractive records the last interactive body for label assertions.
type captureInteractive struct {
	testMessenger
	lastBody string
}

func (m *captureInteractive) SendInteractive(_ context.Context, msg ports.InteractiveMessage) error {
	m.lastBody = msg.Body
	return nil
}
