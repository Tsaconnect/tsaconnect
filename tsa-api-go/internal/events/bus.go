package events

import (
	"log"
	"sync"
)

// HandlerFunc is a function that handles an event.
type HandlerFunc func(event Event)

// Bus is a lightweight channel-based pub/sub event bus.
type Bus struct {
	mu       sync.RWMutex
	handlers map[string][]HandlerFunc
	ch       chan Event
	quit     chan struct{}
}

// NewBus creates and starts a new event bus.
func NewBus() *Bus {
	b := &Bus{
		handlers: make(map[string][]HandlerFunc),
		ch:       make(chan Event, 256),
		quit:     make(chan struct{}),
	}
	go b.loop()
	return b
}

// Subscribe registers a handler for an event type. Use "*" to subscribe to all events.
func (b *Bus) Subscribe(eventType string, handler HandlerFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventType] = append(b.handlers[eventType], handler)
}

// Publish sends an event to the bus. Non-blocking — drops if buffer is full.
func (b *Bus) Publish(event Event) {
	select {
	case b.ch <- event:
	default:
		log.Printf("[EventBus] WARNING: buffer full, dropping event %s for user %s", event.Type, event.UserID)
	}
}

func (b *Bus) loop() {
	for {
		select {
		case event := <-b.ch:
			b.dispatch(event)
		case <-b.quit:
			return
		}
	}
}

func (b *Bus) dispatch(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	// Call specific handlers
	for _, handler := range b.handlers[event.Type] {
		handler(event)
	}
	// Call wildcard handlers
	for _, handler := range b.handlers["*"] {
		handler(event)
	}
}

// Stop shuts down the event bus goroutine.
func (b *Bus) Stop() {
	close(b.quit)
}
