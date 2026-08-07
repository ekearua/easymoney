// Package ratelimit provides fixed-window rate limiting shared across
// replicas via Redis, with an in-memory fallback for single-process runs.
package ratelimit

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limiter counts requests per key within a window. Allow reports whether the
// request may proceed and, when denied, how long the client should wait.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, retryAfter time.Duration)
}

// MemoryLimiter is a per-process fixed-window limiter used when no Redis is
// configured or available.
type MemoryLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	counters map[string]counter
}

type counter struct {
	count    int
	windowID int64
	window   time.Duration
}

// NewMemory creates an in-memory limiter.
func NewMemory() *MemoryLimiter {
	return &MemoryLimiter{
		now:      time.Now,
		counters: map[string]counter{},
	}
}

// Allow implements Limiter with a fixed-window count per key. Stale windows
// are pruned when the counter table grows large.
func (l *MemoryLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	windowID := now.UnixNano() / int64(window)
	entry, ok := l.counters[key]
	if !ok || entry.windowID != windowID {
		entry = counter{windowID: windowID, window: window}
		if len(l.counters) > 10000 {
			for k, e := range l.counters {
				if e.windowID != windowID {
					delete(l.counters, k)
				}
			}
		}
	}
	entry.count++
	if entry.count <= limit {
		l.counters[key] = entry
		return true, 0
	}
	l.counters[key] = entry
	retryAfter := time.Duration(windowID+1)*window - time.Duration(now.UnixNano())
	return false, retryAfter
}

// RedisLimiter is a distributed fixed-window limiter backed by Redis. Counts
// are atomic across replicas; keys expire at the end of each window.
type RedisLimiter struct {
	client *redis.Client
	prefix string
	logger *slog.Logger
}

// NewRedis creates a distributed limiter. The client must already be
// connected; use OpenRedis for a validated connection.
func NewRedis(client *redis.Client, prefix string, logger *slog.Logger) *RedisLimiter {
	return &RedisLimiter{client: client, prefix: prefix, logger: logger}
}

// Allow implements Limiter. On Redis errors the request is allowed through
// (fail-open) and the failure is logged, so an unavailable cache never blocks
// payments.
func (l *RedisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration) {
	now := time.Now()
	windowSeconds := int64(window / time.Second)
	windowID := now.Unix() / windowSeconds
	redisKey := l.prefix + ":" + key + ":" + strconv.FormatInt(windowID, 10)
	script := redis.NewScript(`
		local count = redis.call('INCR', KEYS[1])
		if count == 1 then
			redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[1]))
		end
		return count
	`)
	count, err := script.Run(ctx, l.client, []string{redisKey}, window.Milliseconds()).Int()
	if err != nil {
		l.logger.Error("rate limit redis error", "key", key, "error", err)
		return true, 0
	}
	if count <= limit {
		return true, 0
	}
	windowStart := time.Unix(windowID*windowSeconds, 0)
	retryAfter := window - now.Sub(windowStart)
	return false, retryAfter
}

// Close releases the Redis connection.
func (l *RedisLimiter) Close() error {
	if l.client == nil {
		return nil
	}
	return l.client.Close()
}

// OpenRedis connects to the given URL and verifies connectivity. The returned
// limiter is ready to use.
func OpenRedis(ctx context.Context, url, prefix string, logger *slog.Logger) (*RedisLimiter, error) {
	if url == "" {
		return nil, errors.New("empty redis url")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return NewRedis(client, prefix, logger), nil
}
