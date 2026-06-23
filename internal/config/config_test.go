package config

import (
	"strings"
	"testing"
)

func TestProductionRequiresTestPaystackKey(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ADMIN_PASSWORD_HASH", "hash")
	t.Setenv("PAYSTACK_SECRET_KEY", "sk_live_forbidden")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "verify")
	t.Setenv("WHATSAPP_APP_SECRET", "app")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "access")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "phone")
	t.Setenv("WHATSAPP_GRAPH_VERSION", "v25.0")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "test key") {
		t.Fatalf("expected test-key validation, got %v", err)
	}
}
