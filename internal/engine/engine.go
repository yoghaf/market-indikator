package engine

import (
	"market-indikator/internal/model"
	oi "market-indikator/internal/oi"
	"market-indikator/internal/orderbook"
	"market-indikator/internal/pressure"
	"sync/atomic"
	"unsafe"
)

// =============================================================================
// MULTI-TIMEFRAME AGGREGATION — Mathematical Foundation
// =============================================================================
//
// Each timeframe bucket maintains:
//   OHLC:    standard open/high/low/close
//   BuyVol:  Σ qty where aggressive buy
//   SellVol: Σ qty where aggressive sell
//   Delta:   BuyVol - SellVol
//   AvgScore: EMA of per-tick finalScore within the bucket
//
// TIMEFRAME PRESSURE AGGREGATION:
//   For each HTF bucket, we track an EMA of the tick-level finalScore:
//     AvgScore_t = α·finalScore_t + (1-α)·AvgScore_{t-1}
//   where α = 2/(N+1), N scales with timeframe:
//     5m:  N=50   (α≈0.039)  — moderate smoothing
//     15m: N=100  (α≈0.020)  — more smoothing
//     1h:  N=200  (α≈0.010)  — heavy smoothing
//     4h:  N=500  (α≈0.004)  — very heavy
//     1d:  N=1000 (α≈0.002)  — structural trend
//
//   This gives each timeframe its own responsiveness profile:
//     - 5m score changes quickly → short-term momentum
//     - 1d score changes slowly → structural bias
//
// TRADING INTERPRETATION:
//   Multi-timeframe alignment = highest conviction:
//     If 1s, 5m, 1h, 1d all show score > +40 → strong structural bullish
//     If 1s is bearish but 1h/1d are bullish → counter-trend dip (buy opportunity)
//     If all timeframes converge to 0 → genuine consolidation
//
//   Divergence = caution:
//     If 1s/5m are bullish but 1h/4h are bearish → likely a dead cat bounce
//     If lower TFs flip before higher TFs → early trend change signal
//
// =============================================================================

// CandleDelta holds OHLC + volume delta + pressure EMA for a time bucket.
type CandleDelta struct {
	Time     int64
	Open     float64
	High     float64
	Low      float64
	Close    float64
	BuyVol   float64
	SellVol  float64
	Delta    float64
	AvgScore float64 // EMA of per-tick finalScore within this bucket
	scoreAlpha float64 // EMA alpha for this timeframe
}

// Timeframe definitions: label, bucket duration in seconds, EMA alpha for score
type tfDef struct {
	Seconds int64
	Alpha   float64
}

// We maintain 7 timeframe buckets beyond 1s/1m:
// Index: 0=5m, 1=15m, 2=1h, 3=4h, 4=1d
const NumHTF = 5

var htfDefs = [NumHTF]tfDef{
	{300, 0.039},    // 5m:  N≈50
	{900, 0.020},    // 15m: N≈100
	{3600, 0.010},   // 1h:  N≈200
	{14400, 0.004},  // 4h:  N≈500
	{86400, 0.002},  // 1d:  N≈1000
}

// Engine — integrates all analytics + multi-timeframe candles.
type Engine struct {
	CVD       float64
	LastPrice float64

	Candle1s CandleDelta
	Candle1m CandleDelta
	HTF      [NumHTF]CandleDelta // 5m, 15m, 1h, 4h, 1d

	book     *orderbook.Book
	oiEngine *oi.Engine
	scorer   *pressure.Scorer

	pricePtr unsafe.Pointer
}

func NewEngine(book *orderbook.Book, oiEngine *oi.Engine) *Engine {
	initial := 0.0
	e := &Engine{
		book:     book,
		oiEngine: oiEngine,
		scorer:   pressure.NewScorer(),
	}
	atomic.StorePointer(&e.pricePtr, unsafe.Pointer(&initial))

	// Initialize EMA alphas for HTF buckets
	for i := 0; i < NumHTF; i++ {
		e.HTF[i].scoreAlpha = htfDefs[i].Alpha
	}
	// 1s and 1m use faster alphas
	e.Candle1s.scoreAlpha = 0.333 // N≈5
	e.Candle1m.scoreAlpha = 0.065 // N≈30

	return e
}

func (e *Engine) GetPrice() float64 {
	p := (*float64)(atomic.LoadPointer(&e.pricePtr))
	if p == nil {
		return 0
	}
	return *p
}

// ProcessTrade — HOT PATH.
// ~250ns total: CVD + 7 candle updates + 2 atomic reads + scorer + snapshot.
func (e *Engine) ProcessTrade(t model.Trade) model.Snapshot {
	price := t.Price
	qty := t.Quantity
	tradeTimeSec := t.Time / 1000
	tradeTimeMin := tradeTimeSec / 60 * 60

	// ─── CVD ───
	var delta float64
	if t.IsBuyer {
		delta = -qty
	} else {
		delta = qty
	}
	e.CVD += delta
	e.LastPrice = price

	// ─── PRICE PUBLISH ───
	priceCopy := price
	atomic.StorePointer(&e.pricePtr, unsafe.Pointer(&priceCopy))

	// ─── ORDERBOOK + OI (atomic reads, ~2ns) ───
	press := e.book.GetPressure()
	oiState := e.oiEngine.GetState()

	// ─── COMPOSITE SCORE (~30ns) ───
	finalScore := e.scorer.Update(pressure.Input{
		CVD:        e.CVD,
		Delta1s:    e.Candle1s.Delta,
		OBScore:    press.Score,
		OIDelta1m:  oiState.OIDelta1m,
		OIBehavior: oiState.Behavior,
	})

	// ─── CANDLE UPDATES ───
	// 1s and 1m
	updateCandle(&e.Candle1s, tradeTimeSec, price, qty, delta, finalScore)
	updateCandle(&e.Candle1m, tradeTimeMin, price, qty, delta, finalScore)

	// HTF: 5m, 15m, 1h, 4h, 1d
	for i := 0; i < NumHTF; i++ {
		bucketTime := tradeTimeSec / htfDefs[i].Seconds * htfDefs[i].Seconds
		updateCandle(&e.HTF[i], bucketTime, price, qty, delta, finalScore)
	}

	// ─── BUILD SNAPSHOT ───
	snap := model.Snapshot{
		Price:    price,
		Time:     t.Time,
		CVD:      e.CVD,
		Candle1s: snapshotCandle(&e.Candle1s),
		Candle1m: snapshotCandle(&e.Candle1m),
		Orderbook: model.OrderbookSnapshot{
			BestBid:   press.BestBid,
			BestAsk:   press.BestAsk,
			Spread:    press.Spread,
			Imbalance: press.Imbalance,
			Score:     press.Score,
		},
		OI: model.OISnapshot{
			OI:        oiState.OI,
			OIDelta1s: oiState.OIDelta1s,
			OIDelta1m: oiState.OIDelta1m,
			Behavior:  oiState.Behavior,
		},
		FinalScore: finalScore,
	}

	for i := 0; i < NumHTF; i++ {
		snap.HTF[i] = snapshotCandle(&e.HTF[i])
	}

	return snap
}

// updateCandle — updates a single candle bucket in-place.
// Includes EMA of finalScore for multi-timeframe pressure tracking.
func updateCandle(c *CandleDelta, bucketTime int64, price, qty, delta, score float64) {
	if c.Time != bucketTime {
		// New bucket
		c.Time = bucketTime
		c.Open = price
		c.High = price
		c.Low = price
		c.Close = price
		c.BuyVol = 0
		c.SellVol = 0
		c.Delta = 0
		c.AvgScore = score // Initialize EMA with first score
		return
	}

	if price > c.High {
		c.High = price
	}
	if price < c.Low {
		c.Low = price
	}
	c.Close = price

	if delta > 0 {
		c.BuyVol += qty
	} else {
		c.SellVol += qty
	}
	c.Delta += delta

	// EMA of finalScore within this bucket
	c.AvgScore = c.scoreAlpha*score + (1.0-c.scoreAlpha)*c.AvgScore
}

func snapshotCandle(c *CandleDelta) model.CandleSnapshot {
	return model.CandleSnapshot{
		Time:     c.Time,
		Open:     c.Open,
		High:     c.High,
		Low:      c.Low,
		Close:    c.Close,
		BuyVol:   c.BuyVol,
		SellVol:  c.SellVol,
		Delta:    c.Delta,
		AvgScore: c.AvgScore,
	}
}
