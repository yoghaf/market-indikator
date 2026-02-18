package bus

import (
	"market-indikator/internal/model"
	"sync"
)

// Bus handles internal pub/sub.
type Bus struct {
	mu          sync.RWMutex
	subscribers []chan model.Trade
}

func NewBus() *Bus {
	return &Bus{
		subscribers: make([]chan model.Trade, 0),
	}
}

// Subscribe returns a read-only channel for trades.
func (b *Bus) Subscribe(bufferSize int) <-chan model.Trade {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan model.Trade, bufferSize)
	b.subscribers = append(b.subscribers, ch)
	return ch
}

// Publish broadcasts the trade to all subscribers.
// Non-blocking publish: if a subscriber is slow/full, we drop the message.
func (b *Bus) Publish(t model.Trade) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.subscribers {
		select {
		case ch <- t:
		default:
			// Slow consumer, dropping to maintain low latency
		}
	}
}
