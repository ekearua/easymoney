// Package email provides transactional email delivery adapters.
package email

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
)

// SMTP sends simple plain-text transactional email through a configured SMTP
// server. It is intentionally small because Xego only needs demo onboarding
// codes at this stage.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

// NewSMTP creates an SMTP email sender.
func NewSMTP(host string, port int, username, password, from string) *SMTP {
	return &SMTP{Host: strings.TrimSpace(host), Port: port, Username: strings.TrimSpace(username), Password: password, From: strings.TrimSpace(from)}
}

// Send delivers a plain-text message. The call is wrapped so the caller's
// context can stop waiting if the SMTP server is slow.
func (s *SMTP) Send(ctx context.Context, to, subject, body string) error {
	if s.Host == "" || s.From == "" {
		return fmt.Errorf("SMTP host and from address are required")
	}
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	var auth smtp.Auth
	if s.Username != "" {
		auth = smtp.PlainAuth("", s.Username, s.Password, s.Host)
	}
	message := []byte(strings.Join([]string{
		"From: " + s.From,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n"))
	errCh := make(chan error, 1)
	go func() {
		errCh <- smtp.SendMail(addr, auth, s.From, []string{to}, message)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}
