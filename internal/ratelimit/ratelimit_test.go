package ratelimit

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestMemoryLimiterAllowsUpToLimit(t *testing.T) {
	l := NewMemory()
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		allowed, _ := l.Allow(ctx, "webhook:1.2.3.4", 3, time.Minute)
		if !allowed {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	allowed, retryAfter := l.Allow(ctx, "webhook:1.2.3.4", 3, time.Minute)
	if allowed {
		t.Fatal("request 4 should be denied")
	}
	if retryAfter <= 0 || retryAfter > time.Minute {
		t.Fatalf("retryAfter out of range: %v", retryAfter)
	}
}

func TestMemoryLimiterKeysAreIsolated(t *testing.T) {
	l := NewMemory()
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		allowed, _ := l.Allow(ctx, "a", 5, time.Minute)
		if !allowed {
			t.Fatal("a should be allowed")
		}
	}
	allowed, _ := l.Allow(ctx, "b", 5, time.Minute)
	if !allowed {
		t.Fatal("b should have its own window")
	}
}

func TestMemoryLimiterWindowReset(t *testing.T) {
	l := NewMemory()
	now := time.Now()
	l.now = func() time.Time { return now }
	ctx := context.Background()
	for i := 1; i <= 2; i++ {
		allowed, _ := l.Allow(ctx, "k", 2, time.Minute)
		if !allowed {
			t.Fatal("request within limit")
		}
	}
	if allowed, _ := l.Allow(ctx, "k", 2, time.Minute); allowed {
		t.Fatal("expected denial in same window")
	}
	l.now = func() time.Time { return now.Add(time.Minute) }
	if allowed, _ := l.Allow(ctx, "k", 2, time.Minute); !allowed {
		t.Fatal("new window should reset the counter")
	}
}

func TestMemoryLimiterZeroLimit(t *testing.T) {
	l := NewMemory()
	ctx := context.Background()
	if allowed, _ := l.Allow(ctx, "k", 0, time.Minute); allowed {
		t.Fatal("zero limit should deny every request")
	}
}

func TestOpenRedisEmptyURL(t *testing.T) {
	if _, err := OpenRedis(context.Background(), "", "xego:rl", slog.New(slog.NewTextHandler(os.Stderr, nil))); err == nil {
		t.Fatal("empty url should error")
	}
}

func TestOpenRedisBadURL(t *testing.T) {
	if _, err := OpenRedis(context.Background(), "redis://:", "xego:rl", slog.New(slog.NewTextHandler(os.Stderr, nil))); err == nil {
		t.Fatal("unreachable redis should error")
	}
}

func TestRedisLimiterDistributed(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("set TEST_REDIS_URL to run Redis integration tests")
	}
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	l, err := OpenRedis(ctx, url, "xego:test:rl", logger)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key := "k:" + time.Now().Format(time.RFC3339Nano)
	limit := 2
	window := time.Second
	for i := 1; i <= limit; i++ {
		allowed, _ := l.Allow(ctx, key, limit, window)
		if !allowed {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	if allowed, _ := l.Allow(ctx, key, limit, window); allowed {
		t.Fatal("request past limit should be denied")
	}
	time.Sleep(1100 * time.Millisecond)
	if allowed, _ := l.Allow(ctx, key, limit, window); !allowed {
		t.Fatal("new window should allow again")
	}
}
