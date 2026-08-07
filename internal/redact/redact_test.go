package redact

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestErrorMasksSecrets(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"failed with sk_live_abc1234567890", []string{"sk_[REDACTED]"}},
		{"Authorization: Bearer abcdefghijklmnopqrstuvwxyz123456", []string{"Authorization=[REDACTED]"}},
		{"token=abc123def456ghi789jkl012", []string{"token=[REDACTED]"}},
		{"ref https://user:pass@example.com/order?secret=1x", []string{"[REDACTED]@"}},
		{"card 4111 1111 1111 1111 declined", []string{"[CARD]"}},
		{"card 4111-1111-1111-1111 declined", []string{"[CARD]"}},
		{"phone 08031234567 blocked", []string{"[PHONE]"}},
		{"phone +234 803 123 4567 blocked", []string{"[CARD]"}},
		{"email customer@example.com not found", []string{"c***@example.com"}},
		{"event payload eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.signature", []string{"[REDACTED]"}},
		{"number 01012345678901234567890 long", []string{"[PHONE]"}},
		{"reference aaaaabbbbbcccccdddddeeeee not a token", []string{"aaaaabbbbbcccccdddddeeeee"}},
	}
	for _, tc := range cases {
		got := Error(tc.in, 0)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("Error(%q) = %q, want it to contain %q", tc.in, got, want)
			}
		}
	}
}

func TestErrorDoesNotAlterPlainText(t *testing.T) {
	in := "provider rejected: duplicate transaction reference"
	got := Error(in, 0)
	if got != in {
		t.Errorf("plain text should pass through unchanged, got %q", got)
	}
}

func TestErrorTruncatesAndCollapses(t *testing.T) {
	in := strings.Repeat("x", 300) + "\n\t  y"
	got := Error(in, 100)
	if utf8.RuneCountInString(got) != 100 {
		t.Fatalf("expected 100 runes, got %d: %q", utf8.RuneCountInString(got), got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("control characters should be collapsed: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated string should end with ellipsis: %q", got)
	}
}

func TestErrorEmpty(t *testing.T) {
	if Error("", 0) != "" {
		t.Fatal("empty input should stay empty")
	}
}

func TestErrorShortLimitClamped(t *testing.T) {
	got := Error("abcdefghij", 1)
	if len(got) != 10 {
		t.Fatalf("limit should be clamped to 64 min, got len=%d", len(got))
	}
}

func TestLogValueTruncates(t *testing.T) {
	if LogValue("short", 0) != "short" {
		t.Fatal("short values pass through")
	}
	if LogValue("hello world", 5) != "hell…" {
		t.Fatal("long values are truncated with ellipsis")
	}
}
