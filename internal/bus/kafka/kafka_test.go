package kafka

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

func TestNewRequiresBrokers(t *testing.T) {
	if _, err := New(nil, "xego", nil); !errors.Is(err, ports.ErrBusClosed) {
		t.Fatalf("expected ErrBusClosed, got %v", err)
	}
	if _, err := New([]string{}, "xego", nil); !errors.Is(err, ports.ErrBusClosed) {
		t.Fatalf("expected ErrBusClosed, got %v", err)
	}
}

// TestPublishSubscribeRoundTrip exercises the real Kafka transport. It runs
// only when KAFKA_BROKERS is set (e.g. a local docker-compose broker).
func TestPublishSubscribeRoundTrip(t *testing.T) {
	brokers := splitBrokers(os.Getenv("KAFKA_BROKERS"))
	if len(brokers) == 0 {
		t.Skip("set KAFKA_BROKERS to run the Kafka round-trip test")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus, err := New(brokers, "xego-test", logger)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var got atomic.Int32
	if err := bus.Subscribe(ctx, "xego.test.events", "xego-test", func(_ context.Context, msg ports.EventMessage) error {
		if string(msg.Payload) == "hello-kafka" {
			got.Add(1)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Give the reader time to join the group before publishing.
	time.Sleep(2 * time.Second)
	if err := bus.Publish(ctx, ports.EventMessage{Topic: "xego.test.events", Key: "k1", Payload: []byte("hello-kafka")}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) && got.Load() == 0 {
		time.Sleep(250 * time.Millisecond)
	}
	if got.Load() == 0 {
		t.Fatal("kafka round-trip message not received")
	}
}

func splitBrokers(raw string) []string {
	var out []string
	for _, b := range strings.Split(raw, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}
