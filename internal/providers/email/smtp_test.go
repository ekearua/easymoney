package email

import (
	"context"
	"net/mail"
	"strings"
	"testing"
)

func TestBuildMessageSetsDeliveryHeaders(t *testing.T) {
	from := &mail.Address{Name: "Faya", Address: "no-reply@faya.test"}
	to := &mail.Address{Name: "Ada Obi", Address: "ada@example.com"}

	message, err := buildMessage(from, to, "Set your Faya merchant password", "line one\nline two")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(message)
	for _, want := range []string{
		"From: " + from.String(),
		"To: " + to.String(),
		"Subject: Set your Faya merchant password",
		"Date: ",
		"Message-ID: <",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"\r\n\r\nline one\r\nline two",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("message missing %q:\n%s", want, raw)
		}
	}
	if strings.Contains(strings.ReplaceAll(strings.SplitN(raw, "\r\n\r\n", 2)[0], "\r\n", ""), "\n") {
		t.Fatalf("header block must use CRLF:\n%q", raw)
	}
}

func TestHeaderValueStripsLineBreaks(t *testing.T) {
	if got := headerValue("hello\r\nBcc: attacker@example.com"); strings.ContainsAny(got, "\r\n") {
		t.Fatalf("header injection not stripped: %q", got)
	}
}

func TestNewMessageIDUsesSenderDomain(t *testing.T) {
	id, err := newMessageID("no-reply@faya.test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@faya.test>") {
		t.Fatalf("unexpected Message-ID: %q", id)
	}
}

func TestSendRejectsMalformedAddressesBeforeDialing(t *testing.T) {
	sender := NewSMTP("127.0.0.1", 1, "", "", "not an address")
	if err := sender.Send(context.Background(), "ada@example.com", "subject", "body"); err == nil {
		t.Fatal("expected an error for a malformed From address")
	}

	sender = NewSMTP("127.0.0.1", 1, "", "", "no-reply@faya.test")
	err := sender.Send(context.Background(), "not an address", "subject", "body")
	if err == nil || !strings.Contains(err.Error(), "recipient") {
		t.Fatalf("expected a recipient error, got %v", err)
	}
}
