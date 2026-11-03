// Package email provides transactional email delivery adapters.
package email

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// smtpTimeout bounds a single delivery attempt. Without it a server that
// accepts the connection but stalls mid-conversation can hold the request (and
// the caller's context) open indefinitely.
const smtpTimeout = 30 * time.Second

// implicitTLSPort is the submission port that speaks TLS from the first byte
// (SMTPS). 587 and 25 start in plaintext and upgrade with STARTTLS.
const implicitTLSPort = 465

// SMTP sends simple plain-text transactional email through a configured SMTP
// server. Port 465 uses implicit TLS; every other port must advertise STARTTLS,
// so plaintext delivery is refused.
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

// Send delivers a plain-text message over a TLS connection. The call is
// wrapped so the caller's context can stop waiting if the SMTP server is slow.
func (s *SMTP) Send(ctx context.Context, to, subject, body string) error {
	if s.Host == "" || s.From == "" {
		return fmt.Errorf("SMTP host and from address are required")
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return fmt.Errorf("email recipient is required")
	}
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("SMTP from address %q is invalid: %w", s.From, err)
	}
	recipient, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("email recipient %q is invalid: %w", to, err)
	}
	message, err := buildMessage(from, recipient, subject, body)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.send(ctx, addr, from.Address, recipient.Address, message)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// buildMessage renders the RFC 5322 headers a receiving server expects. Date
// and Message-ID are not optional in practice: filters treat their absence as
// a spam signal, so a message can be accepted by the relay and still never
// reach the inbox.
func buildMessage(from, to *mail.Address, subject, body string) ([]byte, error) {
	messageID, err := newMessageID(from.Address)
	if err != nil {
		return nil, err
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\n", "\r\n")
	return []byte(strings.Join([]string{
		"From: " + headerValue(from.String()),
		"To: " + headerValue(to.String()),
		"Subject: " + headerValue(subject),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"Message-ID: " + messageID,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		body,
	}, "\r\n")), nil
}

// headerValue strips CR/LF so a value can never inject extra headers.
func headerValue(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
}

func newMessageID(from string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate Message-ID: %w", err)
	}
	domain := "localhost"
	if at := strings.LastIndex(from, "@"); at >= 0 && at < len(from)-1 {
		domain = from[at+1:]
	}
	return "<" + hex.EncodeToString(buf) + "@" + domain + ">", nil
}

func (s *SMTP) send(ctx context.Context, addr, envelopeFrom, recipient string, message []byte) error {
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to SMTP server: %w", err)
	}
	deadline := time.Now().Add(smtpTimeout)
	if requestDeadline, ok := ctx.Deadline(); ok && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}
	_ = conn.SetDeadline(deadline)

	config := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	var client *smtp.Client
	if s.Port == implicitTLSPort {
		tlsConn := tls.Client(conn, config)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return fmt.Errorf("SMTP TLS handshake: %w", err)
		}
		client, err = smtp.NewClient(tlsConn, s.Host)
	} else {
		client, err = smtp.NewClient(conn, s.Host)
	}
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP greeting: %w", err)
	}
	defer client.Close()

	if s.Port != implicitTLSPort {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return fmt.Errorf("SMTP server %s does not advertise STARTTLS", addr)
		}
		if err := client.StartTLS(config); err != nil {
			return fmt.Errorf("SMTP STARTTLS: %w", err)
		}
	}

	if s.Username != "" {
		auth := smtp.PlainAuth("", s.Username, s.Password, s.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth: %w", err)
		}
	}
	if err := client.Mail(envelopeFrom); err != nil {
		return fmt.Errorf("SMTP MAIL: %w", err)
	}
	if err := client.Rcpt(recipient); err != nil {
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
