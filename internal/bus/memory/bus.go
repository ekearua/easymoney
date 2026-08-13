// Package memory provides a Kafka-shaped in-memory event bus used by the demo
// and tests. It honours the same delivery contract as the Kafka transport:
// topics with partitions, key-based partition affinity, consumer groups with
// per-partition committed offsets, and at-least-once delivery.
package memory

import (
	"context"
	"hash/fnv"
	"log/slog"
	"sort"
	"sync"
	"time"

	"whatsapp-payment-demo/internal/ports"
)

// DefaultPartitions is the number of partitions assigned to every topic when
// a bus is created without an explicit partition count.
const DefaultPartitions = 4

// retryAttempts bounds redeliveries of a failing message before it is skipped
// so a poison message cannot wedge a consumer.
const retryAttempts = 3

// retryDelay is the backoff between handler retries for a failing message.
const retryDelay = 10 * time.Millisecond

type record struct {
	offset  int64
	key     string
	value   []byte
	headers map[string]string
}

type partition struct {
	mu      sync.Mutex
	records []record
	next    int64
}

func (p *partition) append(key string, value []byte, headers map[string]string) record {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := record{offset: p.next, key: key, value: value, headers: headers}
	p.records = append(p.records, r)
	p.next++
	return r
}

func (p *partition) after(offset int64) []record {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, r := range p.records {
		if r.offset >= offset {
			out := make([]record, len(p.records)-i)
			copy(out, p.records[i:])
			return out
		}
	}
	return nil
}

type topic struct {
	name       string
	partitions []*partition
	subs       map[int64]*subscriber
	nextSubID  int64
}

// group mirrors a Kafka consumer group for one topic. Offsets are committed
// per partition and shared across members; partitions are reassigned whenever
// a member joins or leaves.
type group struct {
	mu       sync.Mutex
	name     string
	topic    *topic
	offsets  map[int]int64
	order    []int64 // subscriber IDs, stable order for round-robin
	assigned map[int64][]int
}

// Bus is a Kafka-compatible in-memory event bus.
type Bus struct {
	partitions int
	logger     *slog.Logger

	mu     sync.Mutex
	topics map[string]*topic
	groups map[string]*group // key "topic\x00group"
	closed bool
}

// New creates an in-memory bus. partitions is the partition count per topic
// (0 selects DefaultPartitions).
func New(partitions int, logger *slog.Logger) *Bus {
	if partitions <= 0 {
		partitions = DefaultPartitions
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Bus{partitions: partitions, logger: logger, topics: map[string]*topic{}, groups: map[string]*group{}}
}

func hashKey(key string) uint32 {
	if key == "" {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return h.Sum32()
}

// Publish appends the message to the partition chosen by its key and makes it
// visible to every consumer group subscribed to the topic.
func (b *Bus) Publish(ctx context.Context, msg ports.EventMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if b.isClosed() {
		return ports.ErrBusClosed
	}
	partitionIndex := int(hashKey(msg.Key) % uint32(b.partitions))
	partition := b.topicOrCreate(msg.Topic).partitions[partitionIndex]
	partition.append(msg.Key, msg.Payload, msg.Headers)
	return nil
}

func (b *Bus) topicOrCreate(name string) *topic {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.topics[name]
	if !ok {
		t = &topic{name: name, subs: map[int64]*subscriber{}}
		for range b.partitions {
			t.partitions = append(t.partitions, &partition{})
		}
		b.topics[name] = t
	}
	return t
}

func (b *Bus) groupOrCreate(groupName string, t *topic) *group {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := groupName + "\x00" + t.name
	if g, ok := b.groups[key]; ok {
		return g
	}
	g := &group{
		name:     groupName,
		topic:    t,
		offsets:  map[int]int64{},
		assigned: map[int64][]int{},
	}
	b.groups[key] = g
	return g
}

// Subscribe registers a consumer group member. The bus assigns it a share of
// the topic's partitions (round-robin across members) and starts a delivery
// loop that runs until ctx is cancelled. Handler failures are redelivered up
// to retryAttempts times, then skipped with a warning.
func (b *Bus) Subscribe(ctx context.Context, topicName, groupName string, handler ports.EventHandler) error {
	t := b.topicOrCreate(topicName)
	g := b.groupOrCreate(groupName, t)

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ports.ErrBusClosed
	}
	sub := &subscriber{id: t.nextSubID, handler: handler, g: g}
	t.nextSubID++
	t.subs[sub.id] = sub
	rebalance(t, g)
	b.mu.Unlock()

	go sub.run(ctx, b)
	return nil
}

// Close stops all delivery loops and releases the bus. Publish after Close
// returns ports.ErrBusClosed.
func (b *Bus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.topics = map[string]*topic{}
	return nil
}

func (b *Bus) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// rebalance assigns partitions round-robin across the group's members. Runs
// with b.mu held (t.subs is stable) and takes g.mu for the group state.
func rebalance(t *topic, g *group) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.order = g.order[:0]
	for id := range t.subs {
		if t.subs[id].g == g {
			g.order = append(g.order, id)
		}
	}
	sort.Slice(g.order, func(i, j int) bool { return g.order[i] < g.order[j] })
	for id := range g.assigned {
		delete(g.assigned, id)
	}
	partitionCount := len(t.partitions)
	for i, id := range g.order {
		assigned := []int{}
		for p := i; p < partitionCount; p += len(g.order) {
			assigned = append(assigned, p)
		}
		g.assigned[id] = assigned
	}
}

type subscriber struct {
	id      int64
	handler ports.EventHandler
	g       *group
}

// assignedPartitions returns the member's current partition assignment, based
// on the latest rebalance.
func (sub *subscriber) assignedPartitions() []int {
	sub.g.mu.Lock()
	defer sub.g.mu.Unlock()
	assigned := sub.g.assigned[sub.id]
	return assigned
}

// committedOffset returns the group's committed offset for a partition.
func (sub *subscriber) committedOffset(partitionIndex int) int64 {
	sub.g.mu.Lock()
	defer sub.g.mu.Unlock()
	return sub.g.offsets[partitionIndex]
}

func (sub *subscriber) commit(partitionIndex int, offset int64) {
	sub.g.mu.Lock()
	defer sub.g.mu.Unlock()
	if offset > sub.g.offsets[partitionIndex] {
		sub.g.offsets[partitionIndex] = offset
	}
}

func (sub *subscriber) run(ctx context.Context, b *Bus) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			b.mu.Lock()
			if t, ok := b.topics[sub.g.topic.name]; ok {
				delete(t.subs, sub.id)
				rebalance(t, sub.g)
			}
			b.mu.Unlock()
			return
		case <-ticker.C:
			if b.isClosed() {
				return
			}
			sub.drain(ctx, b)
		}
	}
}

func (sub *subscriber) drain(ctx context.Context, b *Bus) {
	t := sub.g.topic
	for _, partitionIndex := range sub.assignedPartitions() {
		partition := t.partitions[partitionIndex]
		records := partition.after(sub.committedOffset(partitionIndex))
		for _, r := range records {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if err := sub.deliver(ctx, b, r, partitionIndex); err != nil {
				b.logger.Error("event consumer failed after retries; skipping message",
					"topic", t.name, "group", sub.g.name, "offset", r.offset, "error", err)
			}
			sub.commit(partitionIndex, r.offset+1)
		}
	}
}

// deliver invokes the handler with bounded redelivery on failure.
func (sub *subscriber) deliver(ctx context.Context, b *Bus, r record, partitionIndex int) error {
	msg := ports.EventMessage{Topic: sub.g.topic.name, Key: r.key, Payload: r.value, Headers: r.headers}
	var lastErr error
	for attempt := 0; attempt < retryAttempts; attempt++ {
		if err := sub.handler(ctx, msg); err == nil {
			return nil
		} else {
			lastErr = err
			if attempt < retryAttempts-1 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(retryDelay):
				}
			}
		}
	}
	return lastErr
}
