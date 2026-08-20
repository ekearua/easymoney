package ai

import (
	"context"
	"strings"

	"whatsapp-payment-demo/internal/ports"
)

// Simulated is a deterministic AI provider for development and tests. It
// implements ports.ChatAI, ports.ImageReader, and ports.SpeechToText with
// keyword-based intent matching and canned responses. No API keys required.
type Simulated struct{}

// NewSimulated creates the local AI provider.
func NewSimulated() *Simulated {
	return &Simulated{}
}

// ClassifyIntent returns a deterministic intent based on keyword matching.
func (s *Simulated) ClassifyIntent(_ context.Context, userMessage string, _ []string) (ports.IntentResult, error) {
	lower := strings.ToLower(strings.TrimSpace(userMessage))
	switch {
	case strings.Contains(lower, "pay") || strings.Contains(lower, "send money"):
		return ports.IntentResult{Intent: "pay", Confidence: 0.9, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "data") || strings.Contains(lower, "buy data"):
		return ports.IntentResult{Intent: "buy_data", Confidence: 0.85, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "invoice"):
		return ports.IntentResult{Intent: "invoice", Confidence: 0.85, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "thrift"):
		return ports.IntentResult{Intent: "thrift", Confidence: 0.85, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "individual") || strings.Contains(lower, "become"):
		return ports.IntentResult{Intent: "become_individual", Confidence: 0.8, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "nin") || strings.Contains(lower, "bvn") || strings.Contains(lower, "verify"):
		return ports.IntentResult{Intent: "verify_id", Confidence: 0.8, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "status") || strings.Contains(lower, "check"):
		return ports.IntentResult{Intent: "status", Confidence: 0.7, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "help"):
		return ports.IntentResult{Intent: "help", Confidence: 0.9, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "menu"):
		return ports.IntentResult{Intent: "menu", Confidence: 0.95, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "cancel"):
		return ports.IntentResult{Intent: "cancel", Confidence: 0.9, Entities: map[string]string{}}, nil
	case strings.Contains(lower, "skip"):
		return ports.IntentResult{Intent: "skip", Confidence: 0.9, Entities: map[string]string{}}, nil
	default:
		return ports.IntentResult{Intent: "none", Confidence: 0}, nil
	}
}

// Answer returns a canned response.
func (s *Simulated) Answer(_ context.Context, question string, _ []string) (string, error) {
	lower := strings.ToLower(question)
	switch {
	case strings.Contains(lower, "how") || strings.Contains(lower, "what"):
		return "Xego lets you send money, buy data, create invoices, and join thrift groups — all from WhatsApp. Type MENU to see your options.", nil
	default:
		return "I'm here to help! You can type MENU to see your options, or ask me anything about Xego.", nil
	}
}

// ReadImage simulates OCR by returning a placeholder.
func (s *Simulated) ReadImage(_ context.Context, _ []byte, _, prompt string) (string, error) {
	if strings.Contains(strings.ToLower(prompt), "nin") || strings.Contains(strings.ToLower(prompt), "bvn") {
		return "NIN: 12345678901", nil
	}
	return "Payment reference: REF-12345678 Amount: NGN 5,000 Date: 2026-01-15", nil
}

// Transcribe simulates STT by returning a placeholder.
func (s *Simulated) Transcribe(_ context.Context, _ []byte, _, _ string) (string, error) {
	return "pay 5000 to jumia", nil
}
