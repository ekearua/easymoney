package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"whatsapp-payment-demo/internal/config"
)

// TestGroupByKey pins the fan-out helper's ordering contract: groups appear
// in first-seen key order and each group keeps its original item order, which
// is what lets a claimed batch run in parallel across conversations without
// ever reordering one conversation.
func TestGroupByKey(t *testing.T) {
	t.Parallel()
	type item struct{ key, id string }
	items := []item{
		{"a", "1"}, {"b", "2"}, {"a", "3"}, {"c", "4"}, {"b", "5"}, {"a", "6"},
	}
	groups := groupByKey(items, func(i item) string { return i.key })
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	want := [][]string{{"1", "3", "6"}, {"2", "5"}, {"4"}}
	for i, group := range groups {
		if len(group) != len(want[i]) {
			t.Fatalf("group %d: expected %d items, got %d", i, len(want[i]), len(group))
		}
		for j, item := range group {
			if item.id != want[i][j] {
				t.Errorf("group %d item %d: expected id %s, got %s", i, j, want[i][j], item.id)
			}
		}
	}
	if empty := groupByKey([]item(nil), func(i item) string { return i.key }); empty != nil {
		t.Errorf("expected nil for empty input, got %v", empty)
	}
}

// TestRunPooledNilPool asserts the struct-literal fallback: an App built
// without workerPools (as the webhook tests do) must execute groups serially
// in order instead of deadlocking on a nil-channel send.
func TestRunPooledNilPool(t *testing.T) {
	t.Parallel()
	groups := [][]int{{1, 2}, {3}, {4, 5, 6}}
	var got []int
	runPooled[int](nil, groups, func(group []int) {
		got = append(got, group...)
	})
	want := []int{1, 2, 3, 4, 5, 6}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

// TestDispatchGateSingleFlight asserts the per-handler gate: a tick that
// arrives while the handler is still running is dropped (not queued), and the
// gate reopens once the run finishes.
func TestDispatchGateSingleFlight(t *testing.T) {
	t.Parallel()
	a := &App{workerPools: newWorkerPools(config.Config{
		WorkerInboundConcurrency: 2,
	})}
	release := make(chan struct{})
	var started atomic.Int32
	var ran atomic.Int32

	fn := func(context.Context) {
		started.Add(1)
		<-release
		ran.Add(1)
	}
	a.dispatch(context.Background(), nil, "handler", fn)
	// Wait for the first run to be inside fn, then fire overlapping ticks.
	for started.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 5; i++ {
		a.dispatch(context.Background(), nil, "handler", fn)
	}
	close(release)
	a.workerWg.Wait()

	if got := ran.Load(); got != 1 {
		t.Fatalf("expected exactly 1 run (overlapping ticks dropped), got %d", got)
	}
	// The gate must reopen: a fresh dispatch runs again.
	a.dispatch(context.Background(), nil, "handler", fn)
	a.workerWg.Wait()
	if got := ran.Load(); got != 2 {
		t.Fatalf("expected gate to reopen for a second run, got %d runs", got)
	}
}

// TestDispatchClassSaturation asserts the bounded-class behaviour: when the
// class pool is full, a tick for a *different* handler in the same class is
// skipped instead of queueing.
func TestDispatchClassSaturation(t *testing.T) {
	t.Parallel()
	a := &App{workerPools: newWorkerPools(config.Config{
		WorkerWebhookConcurrency: 1,
	})}
	release := make(chan struct{})
	var started atomic.Int32
	var ranB atomic.Int32

	a.dispatch(context.Background(), a.workerPools.webhook, "hook_a", func(context.Context) {
		started.Add(1)
		<-release
	})
	for started.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	// Pool (size 1) is held by hook_a: hook_b must be dropped, not queued.
	a.dispatch(context.Background(), a.workerPools.webhook, "hook_b", func(context.Context) {
		ranB.Add(1)
	})
	close(release)
	a.workerWg.Wait()
	if got := ranB.Load(); got != 0 {
		t.Fatalf("expected saturated class to drop the tick, hook_b ran %d times", got)
	}
	// After release the slot is free: hook_b runs on the next tick.
	a.dispatch(context.Background(), a.workerPools.webhook, "hook_b", func(context.Context) {
		ranB.Add(1)
	})
	a.workerWg.Wait()
	if got := ranB.Load(); got != 1 {
		t.Fatalf("expected hook_b to run once the slot freed, got %d", got)
	}
}
