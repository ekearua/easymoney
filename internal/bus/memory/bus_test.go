package memory

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

func testBus(t *testing.T) *Bus {
	t.Helper()
	return New(4, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

func TestPublishSubscribeDelivers(t *testing.T) {
	bus := testBus(t)
	defer bus.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got atomic.Int32
	if err := bus.Subscribe(ctx, "payments", "g", func(_ context.Context, msg ports.EventMessage) error {
		got.Add(1)
		if msg.Key != "k1" || msg.Topic != "payments" || string(msg.Payload) != "hello" {
			t.Errorf("unexpected message: %+v", msg)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(ctx, ports.EventMessage{Topic: "payments", Key: "k1", Payload: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return got.Load() == 1 })
}

func TestKeyAffinityPreservesOrder(t *testing.T) {
	bus := testBus(t)
	defer bus.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var seen []string
	var mu int
	if err := bus.Subscribe(ctx, "orders", "g", func(_ context.Context, msg ports.EventMessage) error {
		mu++
		seen = append(seen, string(msg.Payload))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := bus.Publish(ctx, ports.EventMessage{Topic: "orders", Key: "same", Payload: []byte{byte('0' + i)}}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return len(seen) == 10 })
	for i, v := range seen {
		if v != string(rune('0'+i)) {
			t.Fatalf("order broken at %d: got %q want %q", i, v, string(rune('0'+i)))
		}
	}
}

func TestConsumerGroupSplitsPartitions(t *testing.T) {
	bus := testBus(t)
	defer bus.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var total atomic.Int32
	for i := 0; i < 2; i++ {
		if err := bus.Subscribe(ctx, "shared", "workers", func(_ context.Context, msg ports.EventMessage) error {
			total.Add(1)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 24; i++ {
		if err := bus.Publish(ctx, ports.EventMessage{Topic: "shared", Key: "k", Payload: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return total.Load() == 24 })
}

func TestHandlerFailureRetriesThenSkips(t *testing.T) {
	bus := testBus(t)
	defer bus.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	if err := bus.Subscribe(ctx, "fail", "g", func(_ context.Context, msg ports.EventMessage) error {
		calls.Add(1)
		return errors.New("boom")
	}); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(ctx, ports.EventMessage{Topic: "fail", Key: "k", Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	// retryAttempts invocations then the message is skipped and committed.
	waitFor(t, func() bool { return calls.Load() == retryAttempts })
	time.Sleep(30 * time.Millisecond)
	if got := calls.Load(); got != retryAttempts {
		t.Fatalf("handler called %d times, want %d", got, retryAttempts)
	}
}

func TestClosedBusRejectsPublish(t *testing.T) {
	bus := testBus(t)
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
	err := bus.Publish(context.Background(), ports.EventMessage{Topic: "t", Key: "k", Payload: nil})
	if !errors.Is(err, ports.ErrBusClosed) {
		t.Fatalf("expected ErrBusClosed, got %v", err)
	}
}

func TestRedeliveryAfterLateSubscriber(t *testing.T) {
	bus := testBus(t)
	defer bus.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Publish before any subscriber exists.
	for i := 0; i < 4; i++ {
		if err := bus.Publish(ctx, ports.EventMessage{Topic: "late", Key: "k", Payload: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	var got atomic.Int32
	if err := bus.Subscribe(ctx, "late", "g", func(_ context.Context, msg ports.EventMessage) error {
		got.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return got.Load() == 4 })
}
