package service

import (
	"context"
	"strings"
	"sync"
	"testing"

	"whatsapp-payment-demo/internal/config"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

type testMessenger struct {
	mu   sync.Mutex
	text []string
}

func (m *testMessenger) SendText(_ context.Context, _ string, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.text = append(m.text, body)
	return nil
}

func (m *testMessenger) SendInteractive(_ context.Context, msg ports.InteractiveMessage) error { return nil }
func (m *testMessenger) SendCheckout(_ context.Context, _, _, _ string) error                 { return nil }
func (m *testMessenger) SendLink(_ context.Context, _, _, _, _ string) error                  { return nil }
func (m *testMessenger) SendTemplate(_ context.Context, _, _ string, _ []string) error        { return nil }
func (m *testMessenger) SendImage(_ context.Context, _ string, _ []byte, _ string) error      { return nil }

func (m *testMessenger) lastText() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.text) == 0 {
		return ""
	}
	return m.text[len(m.text)-1]
}

func newProfileCompletionTestService() (*ConversationService, *testMessenger) {
	messenger := &testMessenger{}
	cfg := config.Config{WebFlowsEnabled: false}
	svc := NewConversationService(cfg, nil, nil, nil, map[string]ports.Messenger{
		ChannelWhatsApp: messenger,
	}, nil, nil, nil)
	return svc, messenger
}

func TestStartProfileCompletionAlreadyComplete(t *testing.T) {
	t.Parallel()
	svc, messenger := newProfileCompletionTestService()
	user := store.User{DisplayName: "Ada", Email: "ada@example.com"}
	err := svc.startProfileCompletion(context.Background(), ChannelWhatsApp, "+234000000000", user, store.Session{})
	if err != nil {
		t.Fatalf("startProfileCompletion returned error: %v", err)
	}
	if got := messenger.lastText(); !strings.Contains(got, "already has a name and email") {
		t.Fatalf("expected 'already on file' message, got %q", got)
	}
}