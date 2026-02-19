package state

import (
	"market-indikator/internal/model"
	"sync"
)

// RingBuffer — Fixed-size circular buffer for recent market state.
// Stores up to N snapshots (e.g., 3600 = 1 hour).
// Thread-safe for single writer (Engine) and multiple readers (Broadcast).
type RingBuffer struct {
	data     []model.Snapshot
	capacity int
	head     int  // index of the next write
	size     int  // current number of elements
	full     bool // true if buffer has wrapped around
	mu       sync.RWMutex
}

// NewRingBuffer — creates a ring buffer of fixed capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		data:     make([]model.Snapshot, capacity),
		capacity: capacity,
		head:     0,
		size:     0,
		full:     false,
	}
}

// Add — inserts a snapshot. O(1).
func (rb *RingBuffer) Add(snap model.Snapshot) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.data[rb.head] = snap
	rb.head = (rb.head + 1) % rb.capacity

	if rb.full {
		// capacity remains constant, head moved
	} else {
		rb.size++
		if rb.size == rb.capacity {
			rb.full = true
		}
	}
}

// GetAll — returns a copy of all snapshots in chronological order. O(N).
// Used to hydrate new clients.
func (rb *RingBuffer) GetAll() []model.Snapshot {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if rb.size == 0 {
		return nil
	}

	out := make([]model.Snapshot, 0, rb.size)

	if !rb.full {
		// Buffer not full: 0 to head-1
		out = append(out, rb.data[:rb.head]...)
	} else {
		// Buffer full: head to end, then 0 to head-1
		// Head points to the *oldest* data in a full circular buffer (actually head is next write, so head is oldest)
		out = append(out, rb.data[rb.head:]...)
		out = append(out, rb.data[:rb.head]...)
	}

	return out
}

// Size — returns current number of elements.
func (rb *RingBuffer) Size() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.size
}
