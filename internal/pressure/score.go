package pressure

import (
	"math"
)

// =============================================================================
// FINAL COMPOSITE PRESSURE SCORE — Mathematical Foundation
// =============================================================================
//
// This module fuses three orthogonal signal domains into a single
// actionable pressure score in [-100, +100]:
//
//   S_final = clamp( EMA( w_a·S_aggressive + w_p·S_passive + w_pos·S_positioning ), -100, 100 )
//
// ─────────────────────────────────────────────────────────────────────────────
//
// 1) AGGRESSIVE PRESSURE (trade flow)
//    Measures real-time buying/selling aggression from executed trades.
//
//    S_aggressive = α₁·norm(CVD_velocity) + α₂·norm(Delta_1s)
//
//    CVD velocity = change in CVD per second (EMA-smoothed).
//    Delta_1s     = current 1-second candle delta.
//
//    Both are normalized via adaptive z-score:
//      norm(x) = clamp(x / (σ + ε), -1, 1)
//    where σ is a rolling standard deviation (EMA of |x|).
//
//    Weights: α₁=0.6 (CVD momentum), α₂=0.4 (instantaneous delta)
//
// 2) PASSIVE PRESSURE (orderbook)
//    Measures standing liquidity intention from the limit order book.
//
//    S_passive = orderbook_score / 100.0   (already computed, range [-1, +1])
//
//    The orderbook score already incorporates imbalance, liquidity velocity,
//    and absorption. We use it directly.
//
// 3) POSITIONING PRESSURE (open interest)
//    Measures the structural commitment of market participants.
//
//    S_positioning = β₁·norm(ΔOI_1m) + β₂·behavior_signal
//
//    behavior_signal is derived from the OI behavior enum:
//      LONG_BUILDUP    → +1.0  (bullish commitment)
//      SHORT_COVERING  → +0.5  (weakly bullish, no new conviction)
//      NEUTRAL         →  0.0
//      LONG_LIQUIDATION→ -0.5  (weakly bearish, forced exit)
//      SHORT_BUILDUP   → -1.0  (bearish commitment)
//
//    Weights: β₁=0.5 (OI change magnitude), β₂=0.5 (behavioral context)
//
// ─────────────────────────────────────────────────────────────────────────────
//
// DOMAIN WEIGHTS (default, tunable):
//    w_a   = 0.45  — aggressive pressure (highest weight: actual executions)
//    w_p   = 0.30  — passive pressure (standing orders can be spoofed)
//    w_pos = 0.25  — positioning pressure (slower signal, structural)
//
// ─────────────────────────────────────────────────────────────────────────────
//
// EMA SMOOTHING:
//    The raw composite is smoothed with an EMA to reduce noise while
//    preserving responsiveness:
//
//      EMA_t = α·raw_t + (1-α)·EMA_{t-1}
//      α = 2 / (N + 1),  N = smoothing period
//
//    Default N = 5 ticks (~500ms at typical tick rate).
//    This gives α ≈ 0.333, half-life ≈ 2.5 ticks.
//
// ─────────────────────────────────────────────────────────────────────────────
//
// INTERPRETATION:
//    +80 to +100  STRONG BULLISH — aggressive buying + book support + long buildup
//    +40 to  +80  BULLISH — clear directional pressure
//    +10 to  +40  WEAK BULLISH — slight edge
//    -10 to  +10  NEUTRAL / ABSORPTION — balanced or transitioning
//    -40 to  -10  WEAK BEARISH
//    -80 to  -40  BEARISH
//   -100 to  -80  STRONG BEARISH
//
// ─────────────────────────────────────────────────────────────────────────────
//
// ROBUSTNESS (noise, spikes, low liquidity):
//    1. Adaptive normalization: σ adjusts to local volatility regime.
//       In calm markets, small moves produce larger normalized signals.
//       In volatile markets, normalization dampens noise automatically.
//    2. EMA smoothing: single-tick spikes decay with half-life of ~2.5 ticks.
//    3. Multi-domain fusion: a spike in one domain is dampened by the others.
//       News events spike aggressive pressure but orderbook may show absorption,
//       creating a balanced composite.
//
// CALIBRATION GUIDANCE:
//    1. Run the engine for 1+ hours during active market hours (NY/London).
//    2. Log finalScore alongside price. Plot score vs 10-second forward returns.
//    3. If score > +60 consistently predicts positive returns → weights are good.
//    4. If one domain dominates noise → reduce its weight.
//    5. Increase EMA period (N) if score is too noisy; decrease if too laggy.
//    6. The adaptive σ auto-calibrates after ~50 ticks (~5 seconds).
//
// =============================================================================

const (
	// Domain weights — sum to 1.0
	WeightAggressive  = 0.45
	WeightPassive     = 0.30
	WeightPositioning = 0.25

	// Aggressive sub-weights
	AlphaCVD   = 0.60
	AlphaDelta = 0.40

	// Positioning sub-weights
	BetaOIDelta  = 0.50
	BetaBehavior = 0.50

	// EMA smoothing: α = 2/(N+1), N=5 gives α≈0.333
	SmoothingAlpha = 0.333

	// Adaptive normalization EMA decay for σ estimation
	SigmaAlpha = 0.05 // slow adaptation for stability

	// Minimum σ to prevent division by near-zero
	SigmaEpsilon = 0.001
)

// Behavior signal mapping
var behaviorSignal = [5]float64{
	0.0,  // BehaviorNeutral
	1.0,  // BehaviorLongBuildup
	-1.0, // BehaviorShortBuildup
	0.5,  // BehaviorShortCovering
	-0.5, // BehaviorLongLiquidation
}

// Input carries all the raw signals the composite scorer needs.
// Populated from existing engine state — no extra computation.
type Input struct {
	CVD         float64 // running CVD
	Delta1s     float64 // current 1s candle delta
	OBScore     int     // orderbook pressure score [-100, +100]
	OIDelta1m   float64 // OI change over ~1 minute
	OIBehavior  int     // behavior enum (0-4)
}

// Scorer computes the final composite pressure score.
// Called on EVERY trade in the engine goroutine — must be ultra-fast.
// All state is primitive fields — zero allocations.
type Scorer struct {
	// Final output
	FinalScore float64

	// EMA state
	smoothed float64
	hasInit  bool

	// Adaptive normalization state
	prevCVD float64
	cvdVel  float64 // CVD velocity (change per tick)

	// Rolling σ estimates (EMA of |value|)
	sigmaCVDVel float64
	sigmaDelta  float64
	sigmaOI     float64
}

func NewScorer() *Scorer {
	return &Scorer{
		sigmaCVDVel: 1.0, // Initialize to 1.0 to avoid cold-start div-by-zero
		sigmaDelta:  1.0,
		sigmaOI:     1.0,
	}
}

// Update computes the composite score from all signal inputs.
// HOT PATH — ~30ns, zero allocations, pure arithmetic.
func (s *Scorer) Update(in Input) float64 {
	// ─── CVD VELOCITY ───
	s.cvdVel = in.CVD - s.prevCVD
	s.prevCVD = in.CVD

	// ─── ADAPTIVE NORMALIZATION ───
	// Update rolling σ (EMA of absolute values)
	s.sigmaCVDVel = emaUpdate(s.sigmaCVDVel, math.Abs(s.cvdVel), SigmaAlpha)
	s.sigmaDelta = emaUpdate(s.sigmaDelta, math.Abs(in.Delta1s), SigmaAlpha)
	s.sigmaOI = emaUpdate(s.sigmaOI, math.Abs(in.OIDelta1m), SigmaAlpha)

	// Normalize each signal to [-1, +1]
	normCVDVel := adaptiveNorm(s.cvdVel, s.sigmaCVDVel)
	normDelta := adaptiveNorm(in.Delta1s, s.sigmaDelta)
	normOIDelta := adaptiveNorm(in.OIDelta1m, s.sigmaOI)

	// ─── AGGRESSIVE PRESSURE ───
	aggressive := AlphaCVD*normCVDVel + AlphaDelta*normDelta

	// ─── PASSIVE PRESSURE ───
	passive := float64(in.OBScore) / 100.0

	// ─── POSITIONING PRESSURE ───
	behSig := 0.0
	if in.OIBehavior >= 0 && in.OIBehavior < 5 {
		behSig = behaviorSignal[in.OIBehavior]
	}
	positioning := BetaOIDelta*normOIDelta + BetaBehavior*behSig

	// ─── WEIGHTED COMPOSITE ───
	raw := (WeightAggressive*aggressive +
		WeightPassive*passive +
		WeightPositioning*positioning) * 100.0

	// ─── EMA SMOOTHING ───
	if !s.hasInit {
		s.smoothed = raw
		s.hasInit = true
	} else {
		s.smoothed = SmoothingAlpha*raw + (1.0-SmoothingAlpha)*s.smoothed
	}

	// ─── CLAMP TO [-100, +100] ───
	s.FinalScore = clamp(s.smoothed, -100, 100)
	return s.FinalScore
}

// adaptiveNorm normalizes a value using its rolling σ.
// Result is clamped to [-1, +1].
func adaptiveNorm(x, sigma float64) float64 {
	if sigma < SigmaEpsilon {
		sigma = SigmaEpsilon
	}
	return clamp(x/sigma, -1, 1)
}

// emaUpdate computes EMA: new = α·value + (1-α)·prev
func emaUpdate(prev, value, alpha float64) float64 {
	return alpha*value + (1.0-alpha)*prev
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
