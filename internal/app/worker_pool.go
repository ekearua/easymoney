package app

import (
	"context"
	"sync"
	"sync/atomic"

	"whatsapp-payment-demo/internal/config"
)

// This file is the worker-pool core behind requirement #3 of the production
// requirements workbook: the old worker loop ran every handler inline on one
// goroutine, so a slow stage (a stalled provider call, a big inbound batch, a
// retention purge) delayed every other stage behind it, and throughput was
// capped at one batch per handler per cycle no matter the load.
//
// The replacement keeps the ticker select loop but dispatches each handler
// onto its own goroutine, bounded two ways:
//
//   - a per-handler gate: a tick that fires while the previous run of the
//     *same* handler is still going is dropped, never queued. That preserves
//     the old loop's strongest property — one batch of a handler at a time —
//     which the conversation FSM depends on (its session is a
//     load-modify-save cycle), while letting every *other* handler run
//     concurrently.
//   - a per-class slot pool: how many goroutines a class (webhook, data,
//     reconciliation, misc) may run at once, sized from the WORKER_*
//     environment knobs. A tick that finds its class saturated is skipped, so
//     backlog can never hide behind the pools.
//
// Inbound and outbox go one level deeper: their fan-out width is the pool,
// and the claimed batch itself is split by conversation recipient so
// different conversations process in parallel while one conversation stays
// strictly ordered (see groupByKey and processInboundBatch).

// workerGate serialises one named handler across ticks.
type workerGate struct{ running atomic.Bool }

// workerPools carries the bounded slot channels and the per-handler gates.
type workerPools struct {
	// inbound and outbox are fan-out widths used *inside*
	// processInboundMessages and deliverOutbox rather than dispatch classes:
	// the handler gate already guarantees a single batch at a time, and the
	// batch's groups compete for these slots.
	inbound chan struct{}
	outbox  chan struct{}

	webhook chan struct{}
	data    chan struct{}
	recon   chan struct{}
	misc    chan struct{}

	gates sync.Map // handler name -> *workerGate
}

// newWorkerPools sizes every pool from config, flooring each at one slot so
// an unset or invalid knob can never deadlock a handler.
func newWorkerPools(cfg config.Config) workerPools {
	return workerPools{
		inbound: make(chan struct{}, max(cfg.WorkerInboundConcurrency, 1)),
		outbox:  make(chan struct{}, max(cfg.WorkerOutboxConcurrency, 1)),
		webhook: make(chan struct{}, max(cfg.WorkerWebhookConcurrency, 1)),
		data:    make(chan struct{}, max(cfg.WorkerDataConcurrency, 1)),
		recon:   make(chan struct{}, max(cfg.WorkerReconcileConcurrency, 1)),
		misc:    make(chan struct{}, max(cfg.WorkerMiscConcurrency, 1)),
	}
}

// dispatch starts fn for the named handler on its own goroutine unless the
// handler's gate is still busy or the class pool is saturated; either way
// this tick is skipped, never queued. Runs are tracked on workerWg so
// shutdown drains them the way the old inline loop did.
//
// A nil class means the handler bounds itself (inbound/outbox fan out
// internally) and only its gate applies.
func (a *App) dispatch(ctx context.Context, class chan struct{}, name string, fn func(context.Context)) {
	loaded, _ := a.workerPools.gates.LoadOrStore(name, &workerGate{})
	gate := loaded.(*workerGate)
	if !gate.running.CompareAndSwap(false, true) {
		return // previous run still going: drop this tick
	}
	if class != nil {
		select {
		case class <- struct{}{}:
		default:
			gate.running.Store(false)
			return // class saturated: drop this tick
		}
	}
	a.workerWg.Add(1)
	go func() {
		defer a.workerWg.Done()
		defer gate.running.Store(false)
		if class != nil {
			defer func() { <-class }()
		}
		fn(ctx)
	}()
}

// runPooled runs every group on a slot from pool and waits for all of them.
// A nil pool (an App built as a struct literal, as the webhook tests do)
// degrades to serial execution in group order — still correct, just without
// the parallelism, and never a deadlock on a nil-channel send.
func runPooled[T any](pool chan struct{}, groups [][]T, fn func([]T)) {
	if pool == nil {
		for _, group := range groups {
			fn(group)
		}
		return
	}
	var wg sync.WaitGroup
	for _, group := range groups {
		group := group
		wg.Add(1)
		pool <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-pool }()
			fn(group)
		}()
	}
	wg.Wait()
}

// groupByKey splits items into groups sharing a key, preserving both the
// first-seen order of the groups and the original order of items inside a
// group. Callers use it to fan a claimed batch out across the pool:
// different keys run in parallel, one key stays serialized.
func groupByKey[T any](items []T, key func(T) string) [][]T {
	if len(items) == 0 {
		return nil
	}
	index := make(map[string]int, len(items))
	groups := make([][]T, 0, len(items))
	for _, item := range items {
		k := key(item)
		if i, ok := index[k]; ok {
			groups[i] = append(groups[i], item)
			continue
		}
		index[k] = len(groups)
		groups = append(groups, []T{item})
	}
	return groups
}
