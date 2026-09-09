// Package events is an in-process pub/sub bus.
// SMTP ingestion publishes; WebSocket streams + relay engine + audit log
// subscribe. Fan-out is best-effort: a slow subscriber drops events rather
// than back-pressuring ingestion, because losing a live event on a full
// buffer is preferable to slowing down mail acceptance.
package events

import (
	"log/slog"
	"sync"
	"time"
)

// Type is a short, stable string so subscribers filter cheaply.
type Type string

const (
	MessageReceived Type = "message.received"
	MessageDeleted  Type = "message.deleted"
	MessageRelayed  Type = "message.relayed"
	MessageBounced  Type = "message.bounced"
)

// Event is the on-wire shape published to subscribers.
// Data is kept small — a full message row is not sent; consumers refetch
// what they need. This keeps buffers tight and avoids stale payloads.
type Event struct {
	Type       Type      `json:"type"`
	MessageID  string    `json:"message_id,omitempty"`
	MailboxID  string    `json:"mailbox_id,omitempty"`
	Subject    string    `json:"subject,omitempty"`
	FromAddr   string    `json:"from_addr,omitempty"`
	ToAddr     string    `json:"to_addr,omitempty"`
	At         time.Time `json:"at"`
}

// Bus is the pub/sub surface.
type Bus interface {
	Publish(evt Event)
	Subscribe() (ch <-chan Event, unsubscribe func())
	Close()
}

// subBufferSize is deliberately small; live events are ephemeral, so a
// stall of ~1s is enough to signal that a subscriber isn't keeping up.
const subBufferSize = 64

// Memory is an in-process Bus. Safe for concurrent use.
type Memory struct {
	mu     sync.RWMutex
	subs   map[chan Event]struct{}
	closed bool
	log    *slog.Logger
}

// NewMemory returns a ready-to-use Bus.
func NewMemory(log *slog.Logger) *Memory {
	if log == nil {
		log = slog.Default()
	}
	return &Memory{
		subs: make(map[chan Event]struct{}),
		log:  log.With(slog.String("component", "events")),
	}
}

// Publish fans out to every subscriber non-blockingly.
// Slow subscribers see their event dropped and a warning logged.
func (b *Memory) Publish(evt Event) {
	if evt.At.IsZero() {
		evt.At = time.Now().UTC()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for ch := range b.subs {
		select {
		case ch <- evt:
		default:
			b.log.Warn("subscriber slow, dropping event",
				slog.String("type", string(evt.Type)),
			)
		}
	}
}

// Subscribe returns a receive-only channel + an unsubscribe closure.
// The channel is closed by unsubscribe (or Close on the bus).
func (b *Memory) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subBufferSize)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			b.mu.Unlock()
			close(ch)
		})
	}
	return ch, unsub
}

// Close drops all subscribers and closes their channels.
// After Close, Publish is a no-op.
func (b *Memory) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for ch := range b.subs {
		close(ch)
		delete(b.subs, ch)
	}
}
