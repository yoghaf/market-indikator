package orderbook

import (
	"sync/atomic"
	"unsafe"
)

// =============================================================================
// ORDERBOOK PRESSURE ENGINE — Mathematical Foundation
// =============================================================================
//
// This module maintains a real-time L2 orderbook from Binance's partial depth
// stream and computes pressure metrics:
//
// 1) BID/ASK VOLUME IMBALANCE:
//      Imbalance = (BidVol - AskVol) / (BidVol + AskVol)
//    Range: [-1, +1]
//    +1 = all volume on bid side (strong buy pressure)
//    -1 = all volume on ask side (strong sell pressure)
//    We sum the top N levels (default 10) for robustness.
//
// 2) LIQUIDITY VELOCITY (Stacking vs Pulling):
//    Tracks the CHANGE in bid/ask volume between consecutive snapshots.
//      BidVelocity = currentBidVol - previousBidVol
//      AskVelocity = currentAskVol - previousAskVol
//    Positive bid velocity = liquidity stacking (support building)
//    Negative bid velocity = liquidity pulling (support crumbling)
//    Combined into a single signal:
//      LiqVelocity = BidVelocity - AskVelocity
//
// 3) ABSORPTION DETECTION:
//    Absorption occurs when large limit orders absorb aggressive selling/buying
//    without price movement. Heuristic:
//      - Price hasn't moved significantly (< threshold)
//      - But volume has been consumed (bid/ask vol decreased then recovered)
//      - We approximate: high trade volume + stable best bid/ask = absorption
//    We track: if bestBid stays stable across N updates while bidVol fluctuates,
//    we flag absorption.
//      AbsorptionScore = stability_factor × volume_recovery_factor
//
// 4) PRESSURE SCORE (normalized -100 → +100):
//      PressureScore = clamp(
//        w1 * Imbalance * 100 +
//        w2 * normalize(LiqVelocity) +
//        w3 * AbsorptionSignal,
//        -100, +100
//      )
//    Default weights: w1=0.5, w2=0.3, w3=0.2
//
// =============================================================================

const (
	MaxDepthLevels  = 20 // we track top 20 levels
	ImbalanceLevels = 10 // use top 10 for imbalance calc
)

// PriceLevel is a single bid or ask level.
type PriceLevel struct {
	Price    float64
	Quantity float64
}

// Pressure is the computed analytics snapshot, designed for atomic swapping.
// This struct is small enough to be stack-allocated and shared via atomic pointer.
type Pressure struct {
	BestBid   float64 // Best bid price
	BestAsk   float64 // Best ask price
	Spread    float64 // BestAsk - BestBid
	BidVol    float64 // Total bid volume (top N levels)
	AskVol    float64 // Total ask volume (top N levels)
	Imbalance float64 // [-1, +1] volume imbalance
	LiqVel    float64 // Liquidity velocity (bid growth - ask growth)
	Absorb    float64 // Absorption score [0, 1]
	Score     int     // Pressure score [-100, +100]
}

// Book maintains the L2 orderbook and computes pressure metrics.
// It is owned by a SINGLE goroutine (the depth ingest goroutine).
// The computed Pressure is shared with other goroutines via atomic pointer.
type Book struct {
	Bids [MaxDepthLevels]PriceLevel
	Asks [MaxDepthLevels]PriceLevel
	BidN int // number of active bid levels
	AskN int // number of active ask levels

	// Previous state for velocity calculation
	prevBidVol float64
	prevAskVol float64

	// Absorption tracking
	prevBestBid    float64
	bidStableCount int
	bidVolRecovery float64

	prevBestAsk    float64
	askStableCount int
	askVolRecovery float64

	// Atomic pointer for lock-free sharing with engine goroutine
	pressure unsafe.Pointer // *Pressure
}

func NewBook() *Book {
	b := &Book{}
	initial := &Pressure{}
	atomic.StorePointer(&b.pressure, unsafe.Pointer(initial))
	return b
}

// GetPressure returns the latest pressure snapshot.
// LOCK-FREE: uses atomic load, safe for concurrent reads from any goroutine.
// ~1ns latency.
func (b *Book) GetPressure() Pressure {
	p := (*Pressure)(atomic.LoadPointer(&b.pressure))
	return *p
}

// UpdateDepth replaces the full depth snapshot (from Binance partial depth stream).
// Called from the depth ingest goroutine ONLY — single writer, no locks needed.
//
// bids and asks are sorted by price (bids descending, asks ascending) from Binance.
func (b *Book) UpdateDepth(bids, asks []PriceLevel) {
	// Copy into fixed arrays (zero allocation, just field writes)
	b.BidN = min(len(bids), MaxDepthLevels)
	for i := 0; i < b.BidN; i++ {
		b.Bids[i] = bids[i]
	}

	b.AskN = min(len(asks), MaxDepthLevels)
	for i := 0; i < b.AskN; i++ {
		b.Asks[i] = asks[i]
	}

	// Compute metrics and publish atomically
	b.computeAndPublish()
}

func (b *Book) computeAndPublish() {
	p := &Pressure{}

	if b.BidN == 0 || b.AskN == 0 {
		atomic.StorePointer(&b.pressure, unsafe.Pointer(p))
		return
	}

	// ─── BEST BID/ASK ───
	p.BestBid = b.Bids[0].Price
	p.BestAsk = b.Asks[0].Price
	p.Spread = p.BestAsk - p.BestBid

	// ─── VOLUME SUMS (top N levels) ───
	levels := min(ImbalanceLevels, b.BidN)
	for i := 0; i < levels; i++ {
		p.BidVol += b.Bids[i].Quantity
	}
	levels = min(ImbalanceLevels, b.AskN)
	for i := 0; i < levels; i++ {
		p.AskVol += b.Asks[i].Quantity
	}

	// ─── IMBALANCE ───
	total := p.BidVol + p.AskVol
	if total > 0 {
		p.Imbalance = (p.BidVol - p.AskVol) / total
	}

	// ─── LIQUIDITY VELOCITY ───
	if b.prevBidVol > 0 || b.prevAskVol > 0 {
		bidDelta := p.BidVol - b.prevBidVol
		askDelta := p.AskVol - b.prevAskVol
		p.LiqVel = bidDelta - askDelta
	}
	b.prevBidVol = p.BidVol
	b.prevAskVol = p.AskVol

	// ─── ABSORPTION DETECTION ───
	// Bid absorption: best bid stable + bid volume recovered after dip
	absorb := 0.0
	if b.prevBestBid > 0 {
		if p.BestBid == b.prevBestBid {
			b.bidStableCount++
		} else {
			b.bidStableCount = 0
		}
	}
	if b.prevBestAsk > 0 {
		if p.BestAsk == b.prevBestAsk {
			b.askStableCount++
		} else {
			b.askStableCount = 0
		}
	}

	// Absorption signal: stability × volume maintained despite pressure
	// Max stability factor at 10 consecutive stable updates
	bidStability := clampF(float64(b.bidStableCount)/10.0, 0, 1)
	askStability := clampF(float64(b.askStableCount)/10.0, 0, 1)

	// Net absorption: bid absorption is bullish (+), ask absorption is bearish (-)
	absorb = bidStability - askStability
	p.Absorb = clampF(absorb, -1, 1)

	b.prevBestBid = p.BestBid
	b.prevBestAsk = p.BestAsk

	// ─── PRESSURE SCORE [-100, +100] ───
	// Weighted combination of signals
	const (
		w1 = 0.50 // imbalance weight
		w2 = 0.30 // liquidity velocity weight
		w3 = 0.20 // absorption weight
	)

	// Normalize liquidity velocity to roughly [-1, 1] range
	// Using a soft normalization: tanh-like with scale factor
	liqNorm := clampF(p.LiqVel/100.0, -1, 1) // 100 BTC change = max signal

	raw := w1*p.Imbalance*100 +
		w2*liqNorm*100 +
		w3*p.Absorb*100

	p.Score = clampI(int(raw), -100, 100)

	// Atomic publish — engine goroutine sees this immediately on next read
	atomic.StorePointer(&b.pressure, unsafe.Pointer(p))
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampI(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
