// Package redact sanitizes free-form error and log strings before they are
// persisted or emitted, so provider error text can never leak API keys, card
// numbers, phone numbers, or emails (C30, C5 log masking policy).
package redact

import (
	"regexp"
	"strings"
	"unicode"
)

const (
	// DefaultMaxLen caps persisted error text. Longer messages are truncated.
	DefaultMaxLen = 500
	// LogMaxLen caps error text in log records.
	LogMaxLen = 300
)

var (
	longTokenRE  = regexp.MustCompile(`\b[A-Za-z0-9+/_\-]{24,}\b`)
	digitsRunRE  = regexp.MustCompile(`\d{10,}`)
	emailRE      = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	bearerRE     = regexp.MustCompile(`(?i)\b(bearer|token|secret|authorization|api[-_ ]?key)[=:\s]+[^\s,;]+`)
	urlRE        = regexp.MustCompile(`https?://[^\s"'<>]+`)
	urlCredsRE   = regexp.MustCompile(`(https?://)[^/@\s]+@`)
	skKeyRE      = regexp.MustCompile(`(?i)\bsk_[A-Za-z0-9_]{10,}\b`)
	phoneRE      = regexp.MustCompile(`(?:\+?\d{1,3}[\s\-]?)?\(?\d{3}\)?[\s\-]?\d{3}[\s\-]?\d{4}`)
	panRE        = regexp.MustCompile(`\b(?:\d[ \-]?){13,19}\b`)
	controlRE    = regexp.MustCompile(`[\r\n\t]+`)
	multiSpaceRE = regexp.MustCompile(`[ \t]{2,}`)
)

// isHighEntropyToken reports whether a candidate token looks like a secret or
// credential rather than ordinary prose: it must mix at least two character
// classes (digits, lowercase, uppercase, symbols).
func isHighEntropyToken(s string) bool {
	var digit, lower, upper, symbol bool
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		default:
			symbol = true
		}
	}
	classes := 0
	for _, b := range []bool{digit, lower, upper, symbol} {
		if b {
			classes++
		}
	}
	return classes >= 2
}

func maskLongDigits(s string) string {
	return digitsRunRE.ReplaceAllStringFunc(s, func(run string) string {
		run = strings.TrimSpace(run)
		if len(run) <= 10 {
			return run
		}
		return run[:3] + strings.Repeat("*", len(run)-6) + run[len(run)-3:]
	})
}

// Error returns a sanitized copy of provider error text: secrets and PII are
// masked, whitespace is collapsed, and the result is truncated to maxLen
// (clamped to [64, 2000], 0 means DefaultMaxLen).
func Error(s string, maxLen int) string {
	if s == "" {
		return ""
	}
	if maxLen == 0 {
		maxLen = DefaultMaxLen
	}
	if maxLen < 64 {
		maxLen = 64
	}
	if maxLen > 2000 {
		maxLen = 2000
	}
	s = strings.TrimSpace(s)
	s = skKeyRE.ReplaceAllString(s, "sk_[REDACTED]")
	s = bearerRE.ReplaceAllString(s, "$1=[REDACTED]")
	s = urlCredsRE.ReplaceAllString(s, "${1}[REDACTED]@")
	s = urlRE.ReplaceAllStringFunc(s, func(u string) string {
		if strings.Contains(u, "?") {
			return strings.SplitN(u, "?", 2)[0] + "?[REDACTED]"
		}
		return u
	})
	s = longTokenRE.ReplaceAllStringFunc(s, func(tok string) string {
		if isHighEntropyToken(tok) {
			return "[REDACTED]"
		}
		return tok
	})
	s = panRE.ReplaceAllString(s, "[CARD]")
	s = phoneRE.ReplaceAllString(s, "[PHONE]")
	s = maskLongDigits(s)
	s = emailRE.ReplaceAllStringFunc(s, func(email string) string {
		at := strings.Index(email, "@")
		if at <= 1 {
			return "[REDACTED]"
		}
		return email[:1] + "***" + email[at:]
	})
	s = controlRE.ReplaceAllString(s, " ")
	s = multiSpaceRE.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxLen {
		s = strings.TrimSpace(string(r[:maxLen-1])) + "…"
	}
	return s
}

// LogValue formats a value for structured logs, masking digits that are
// meaningless as numeric identifiers (sizes, offsets) only in the caller's
// judgment; this helper exists so callers can consistently cap log fields.
func LogValue(s string, maxLen int) string {
	if maxLen == 0 {
		maxLen = LogMaxLen
	}
	r := []rune(strings.TrimSpace(s))
	if len(r) > maxLen {
		return string(r[:maxLen-1]) + "…"
	}
	return string(r)
}

// IsPrintable guards templates and chat replies from control characters.
func IsPrintable(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}
