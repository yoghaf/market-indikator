package oi

import (
	"sync/atomic"
	"unsafe"
)

// =============================================================================
// OPEN INTEREST INTELLIGENCE — Mathematical Foundation
// =============================================================================
//
// Open Interest (OI) = total number of outstanding derivative contracts.
// Changes in OI combined with price movement reveal market participant behavior:
//
// BEHAVIOR CLASSIFICATION (2x2 matrix):
//
//   Price Direction × OI Direction → Behavior
//   ──────────────────────────────────────────
//   Price ↑  ×  OI ↑  →  LONG BUILDUP
//     New long positions are being opened. Buyers are aggressive and confident.
//     Bullish signal — fresh money entering on the long side.
//
//   Price ↓  ×  OI ↑  →  SHORT BUILDUP
//     New short positions are being opened. Sellers are aggressive and confident.
//     Bearish signal — fresh money entering on the short side.
//
//   Price ↑  ×  OI ↓  →  SHORT COVERING
//     Existing short positions are being closed (buying to cover).
//     Weakly bullish — price rises on position unwinding, not new conviction.
//
//   Price ↓  ×  OI ↓  →  LONG LIQUIDATION
//     Existing long positions are being closed (forced or voluntary).
//     Weakly bearish — longs exiting, often cascading.
//
// NEUTRAL: when OI or price change is negligible (below threshold).
//
// OI DELTA:
//   We compute short-term OI change rate:
//     OIDelta1s = current_OI - OI_1_second_ago
//     OIDelta1m = current_OI - OI_1_minute_ago
//
//   These are stored in ring buffers indexed by unix seconds/minutes.
//
// =============================================================================

// Behavior classification enum
const (
	BehaviorNeutral     = 0
	BehaviorLongBuildup = 1
	BehaviorShortBuildup = 2
	BehaviorShortCovering = 3
	BehaviorLongLiquidation = 4
)

// State is the computed OI analytics, shared via atomic pointer.
type State struct {
	OI         float64 // Current open interest (contracts)
	OIDelta1s  float64 // OI change in last ~3s (poll interval)
	OIDelta1m  float64 // OI change in last ~1m
	Behavior   int     // BehaviorXxx enum
	PriceAtOI  float64 // Price when OI was last sampled
}

// Engine maintains OI state and computes behavior classification.
// Written by a SINGLE goroutine (the OI poller). Read by the engine goroutine
// via atomic pointer (lock-free).
type Engine struct {
	state unsafe.Pointer // *State

	// Previous values for delta computation
	prevOI    float64
	prevPrice float64

	// Ring buffer for 1-minute delta (store OI at each poll, ~20 entries for 3s interval)
	ring    [20]float64
	ringIdx int
	ringLen int
}

func NewEngine() *Engine {
	e := &Engine{}
	initial := &State{}
	atomic.StorePointer(&e.state, unsafe.Pointer(initial))
	return e
}

// GetState returns the latest OI state.
// LOCK-FREE: atomic load, ~1ns.
func (e *Engine) GetState() State {
	p := (*State)(atomic.LoadPointer(&e.state))
	return *p
}

// Update is called by the OI poller goroutine with fresh data.
// currentPrice is the latest price from the trade engine (passed in by main).
func (e *Engine) Update(oi float64, currentPrice float64) {
	s := &State{
		OI:        oi,
		PriceAtOI: currentPrice,
	}

	// ─── OI DELTA (short-term: vs previous poll) ───
	if e.prevOI > 0 {
		s.OIDelta1s = oi - e.prevOI
	}

	// ─── OI DELTA (1-minute: ring buffer) ───
	// Ring buffer stores OI values at each poll interval (~3s).
	// 20 entries × 3s = ~60s lookback.
	if e.ringLen >= 20 {
		// Compare to value 20 polls ago (~1 minute)
		s.OIDelta1m = oi - e.ring[e.ringIdx]
	}
	e.ring[e.ringIdx] = oi
	e.ringIdx = (e.ringIdx + 1) % 20
	if e.ringLen < 20 {
		e.ringLen++
	}

	// ─── BEHAVIOR CLASSIFICATION ───
	if e.prevOI > 0 && e.prevPrice > 0 {
		oiChange := oi - e.prevOI
		priceChange := currentPrice - e.prevPrice

		// Thresholds to avoid noise
		// OI must change by at least 0.01% of current OI
		oiThreshold := e.prevOI * 0.0001
		// Price must change by at least $1
		priceThreshold := 1.0

		oiUp := oiChange > oiThreshold
		oiDown := oiChange < -oiThreshold
		priceUp := priceChange > priceThreshold
		priceDown := priceChange < -priceThreshold

		switch {
		case priceUp && oiUp:
			s.Behavior = BehaviorLongBuildup
		case priceDown && oiUp:
			s.Behavior = BehaviorShortBuildup
		case priceUp && oiDown:
			s.Behavior = BehaviorShortCovering
		case priceDown && oiDown:
			s.Behavior = BehaviorLongLiquidation
		default:
			s.Behavior = BehaviorNeutral
		}
	}

	e.prevOI = oi
	e.prevPrice = currentPrice

	// Atomic publish
	atomic.StorePointer(&e.state, unsafe.Pointer(s))
}
