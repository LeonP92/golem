package sse

import "sync"

// LogEntryEvent is an event published for a log entry associated with a ticket.
type LogEntryEvent struct {
	SequenceNum uint
	EntryType   string
	FromRole    string
	ToRole      string
	Message     string
}

// Broker fans out LogEntryEvents to per-ticket subscribers.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[string][]chan LogEntryEvent
}

// NewBroker creates an empty Broker.
func NewBroker() *Broker {
	return &Broker{subscribers: make(map[string][]chan LogEntryEvent)}
}

// Subscribe registers interest in events for ticketID. The returned cancel
// function must be called to unsubscribe and close the channel.
func (b *Broker) Subscribe(ticketID string) (<-chan LogEntryEvent, func()) {
	ch := make(chan LogEntryEvent, 64)
	b.mu.Lock()
	b.subscribers[ticketID] = append(b.subscribers[ticketID], ch)
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		subs := b.subscribers[ticketID]
		for i, c := range subs {
			if c == ch {
				b.subscribers[ticketID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		close(ch)
	}
	return ch, cancel
}

// Publish sends an event to all subscribers for ticketID. Drops the event if a
// subscriber's buffer is full.
func (b *Broker) Publish(ticketID string, evt LogEntryEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers[ticketID] {
		select {
		case ch <- evt:
		default:
		}
	}
}

// SubscriberCount returns the number of live subscribers for ticketID.
//
// Exposed so that transports can be tested for the leak that matters: a
// handler that returns without calling its cancel func keeps a channel in the
// map forever, and Publish walks every subscriber on every entry, so the cost
// is paid by all future log traffic rather than by the connection that leaked.
func (b *Broker) SubscriberCount(ticketID string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers[ticketID])
}
