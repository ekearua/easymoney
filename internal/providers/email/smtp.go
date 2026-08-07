// Package email provides transactional email delivery adapters.
package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
)

// SMTP sends simple plain-text transactional email through a configured SMTP
// server using STARTTLS. It is intentionally small because Xego only needs
// demo onboarding codes at this stage.
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

// Send delivers a plain-text message over a TLS-upgraded connection. The call
// is wrapped so the caller's context can stop waiting if the SMTP server is
// slow. STARTTLS is mandatory: plaintext delivery is refused.
func (s *SMTP) Send(ctx context.Context, to, subject, body string) error {
	if s.Host == "" || s.From == "" {
		return fmt.Errorf("SMTP host and from address are required")
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
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
		errCh <- s.send(ctx, addr, message, to)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func (s *SMTP) send(ctx context.Context, addr string, message []byte, to string) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to SMTP server: %w", err)
	}
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP greeting: %w", err)
	}
	defer client.Close()

	ok, _ := client.Extension("STARTTLS")
	if !ok {
		return fmt.Errorf("SMTP server %s does not advertise STARTTLS", addr)
	}
	config := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	if err := client.StartTLS(config); err != nil {
		return fmt.Errorf("SMTP STARTTLS: %w", err)
	}

	if s.Username != "" {
		auth := smtp.PlainAuth("", s.Username, s.Password, s.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth: %w", err)
		}
	}
	if err := client.Mail(s.From); err != nil {
		return fmt.Errorf("SMTP MAIL: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP RCPT: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := writer.Write(message); err != nil {
		writer.Close()
		return fmt.Errorf("SMTP body write: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("SMTP body close: %w", err)
	}
	return client.Quit()
}
