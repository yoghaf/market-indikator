package model

import (
	"math"
)

// Trade represents a single trade event from Binance Futures.
// efficient memory layout.
type Trade struct {
	ID       int64
	Price    float64
	Quantity float64
	Time     int64
	IsBuyer  bool // true if buyer is maker (aggTrade 'm')
}

// AppendMsgPack appends the MsgPack representation of the Trade to the provided buffer.
// This allows us to reuse a single broadcaster buffer for all clients.
// We use a fixed-size array format for compactness and speed.
// Format: FixArray(5) [ID, Price, Quantity, Time, IsBuyer]
func (t *Trade) AppendMsgPack(b []byte) []byte {
	// Array of 5 elements: 0x95
	b = append(b, 0x95)

	// 1. ID (int64)
	b = appendInt64(b, t.ID)

	// 2. Price (float64)
	b = append(b, 0xcb) // float64 marker
	bits := math.Float64bits(t.Price)
	b = append(b, byte(bits>>56), byte(bits>>48), byte(bits>>40), byte(bits>>32),
		byte(bits>>24), byte(bits>>16), byte(bits>>8), byte(bits))

	// 3. Quantity (float64)
	b = append(b, 0xcb) // float64 marker
	bits = math.Float64bits(t.Quantity)
	b = append(b, byte(bits>>56), byte(bits>>48), byte(bits>>40), byte(bits>>32),
		byte(bits>>24), byte(bits>>16), byte(bits>>8), byte(bits))

	// 4. Time (int64)
	b = appendInt64(b, t.Time)

	// 5. IsBuyer (bool)
	if t.IsBuyer {
		b = append(b, 0xc3) // true
	} else {
		b = append(b, 0xc2) // false
	}

	return b
}

func appendInt64(b []byte, v int64) []byte {
	// positive fixint
	if v >= 0 && v <= 127 {
		return append(b, byte(v))
	}
	// negative fixint
	if v < 0 && v >= -32 {
		return append(b, byte(v))
	}
	// We'll just use int64 (0xd3) for everything else to be safe and simple for now.
	// Optimization: could add uint64/int32/etc checks.
	b = append(b, 0xd3)
	b = append(b, byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32),
		byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	return b
}
