// Package kafka implements the event-bus port with a real Kafka transport
// backed by segmentio/kafka-go. It mirrors the delivery contract of the
// in-memory bus: key-based partition affinity, consumer groups with committed
// offsets, and at-least-once delivery.
package kafka

import (
	"context"
	"log/slog"
	"sync"

	kafkago "github.com/segmentio/kafka-go"

	"whatsapp-payment-demo/internal/ports"
)

// Bus is a Kafka-backed event bus.
type Bus struct {
	brokers []string
	groupID string
	logger  *slog.Logger

	writer  *kafkago.Writer
	mu      sync.Mutex
	readers map[string]*kafkago.Reader // key "topic\x00group"
	closed  bool
}

// New creates a Kafka event bus. brokers must contain at least one bootstrap
// address. groupID is the default consumer group used by Subscribe.
func New(brokers []string, groupID string, logger *slog.Logger) (*Bus, error) {
	if len(brokers) == 0 {
		return nil, ports.ErrBusClosed
	}
	if logger == nil {
		logger = slog.Default()
	}
	writer := &kafkago.Writer{
		Addr:     kafkago.TCP(brokers...),
		Balancer: &kafkago.Hash{},
	}
	return &Bus{brokers: brokers, groupID: groupID, logger: logger, writer: writer, readers: map[string]*kafkago.Reader{}}, nil
}

// Publish writes the message to its topic; the key selects the partition so
// events for the same key stay ordered.
func (b *Bus) Publish(ctx context.Context, msg ports.EventMessage) error {
	kmsg := kafkago.Message{
		Topic: msg.Topic,
		Key:   []byte(msg.Key),
		Value: msg.Payload,
	}
	for k, v := range msg.Headers {
		kmsg.Headers = append(kmsg.Headers, kafkago.Header{Key: k, Value: []byte(v)})
	}
	return b.writer.WriteMessages(ctx, kmsg)
}

// Subscribe consumes a topic in a consumer group and delivers to handler until
// ctx is cancelled. Offsets are committed only after the handler succeeds,
// giving at-least-once semantics.
func (b *Bus) Subscribe(ctx context.Context, topic, group string, handler ports.EventHandler) error {
	if group == "" {
		group = b.groupID
	}
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers: b.brokers,
		Topic:   topic,
		GroupID: group,
	})
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		_ = reader.Close()
		return ports.ErrBusClosed
	}
	b.readers[topic+"\x00"+group] = reader
	b.mu.Unlock()

	go b.deliver(ctx, reader, topic, group, handler)
	return nil
}

func (b *Bus) deliver(ctx context.Context, reader *kafkago.Reader, topic, group string, handler ports.EventHandler) {
	for {
		message, err := reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				_ = reader.Close()
				return
			}
			b.logger.Error("kafka fetch failed", "topic", topic, "group", group, "error", err)
			continue
		}
		msg := ports.EventMessage{
			Topic:   topic,
			Key:     string(message.Key),
			Payload: message.Value,
			Headers: map[string]string{},
		}
		for _, h := range message.Headers {
			msg.Headers[h.Key] = string(h.Value)
		}
		if err := handler(ctx, msg); err != nil {
			// Do not commit; the message will be redelivered on the next fetch.
			b.logger.Error("kafka event handler failed; message will be redelivered",
				"topic", topic, "group", group, "partition", message.Partition, "offset", message.Offset, "error", err)
			continue
		}
		if err := reader.CommitMessages(ctx, message); err != nil {
			b.logger.Error("kafka commit failed", "topic", topic, "group", group, "error", err)
		}
	}
}

// Close shuts down the producer and every reader.
func (b *Bus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	_ = b.writer.Close()
	for _, reader := range b.readers {
		_ = reader.Close()
	}
	b.readers = map[string]*kafkago.Reader{}
	return nil
}
