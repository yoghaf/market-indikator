package model

import (
	"math"
)

// CandleSnapshot — point-in-time copy of a candle bucket.
// Now includes AvgScore (EMA of finalScore within the bucket).
type CandleSnapshot struct {
	Time     int64
	Open     float64
	High     float64
	Low      float64
	Close    float64
	BuyVol   float64
	SellVol  float64
	Delta    float64
	AvgScore float64 // EMA of per-tick finalScore
}

type OrderbookSnapshot struct {
	BestBid   float64
	BestAsk   float64
	Spread    float64
	Imbalance float64
	Score     int
}

type OISnapshot struct {
	OI        float64
	OIDelta1s float64
	OIDelta1m float64
	Behavior  int
}

// NumHTF is the number of higher timeframe buckets.
const NumHTF = 5

// Snapshot — full enriched state broadcast on each trade.
//
// MsgPack wire format: FixArray(9)
//   [0] price      float64
//   [1] cvd        float64
//   [2] time       int64
//   [3] candle1s   FixArray(9) [time, o, h, l, c, buyVol, sellVol, delta, avgScore]
//   [4] candle1m   FixArray(9)
//   [5] orderbook  FixArray(5) [bestBid, bestAsk, spread, imbalance, score]
//   [6] oi         FixArray(4) [oi, oiDelta1s, oiDelta1m, behavior]
//   [7] finalScore float64
//   [8] htf        FixArray(5) — each is FixArray(9) [5m, 15m, 1h, 4h, 1d]
type Snapshot struct {
	Price      float64
	Time       int64
	CVD        float64
	Candle1s   CandleSnapshot
	Candle1m   CandleSnapshot
	Orderbook  OrderbookSnapshot
	OI         OISnapshot
	FinalScore float64
	HTF        [NumHTF]CandleSnapshot
}

// AppendMsgPack — ZERO heap allocations.
func (s *Snapshot) AppendMsgPack(b []byte) []byte {
	b = append(b, 0x99) // FixArray(9)

	b = appendFloat64(b, s.Price)
	b = appendFloat64(b, s.CVD)
	b = appendInt64(b, s.Time)
	b = appendCandleSnapshot(b, &s.Candle1s)
	b = appendCandleSnapshot(b, &s.Candle1m)
	b = appendOrderbookSnapshot(b, &s.Orderbook)
	b = appendOISnapshot(b, &s.OI)
	b = appendFloat64(b, s.FinalScore)

	// HTF array: FixArray(5), each element is a candle
	b = append(b, 0x95) // FixArray(5)
	for i := 0; i < NumHTF; i++ {
		b = appendCandleSnapshot(b, &s.HTF[i])
	}

	return b
}

// Candle: FixArray(9) — now includes avgScore
func appendCandleSnapshot(b []byte, c *CandleSnapshot) []byte {
	b = append(b, 0x99) // FixArray(9)
	b = appendInt64(b, c.Time)
	b = appendFloat64(b, c.Open)
	b = appendFloat64(b, c.High)
	b = appendFloat64(b, c.Low)
	b = appendFloat64(b, c.Close)
	b = appendFloat64(b, c.BuyVol)
	b = appendFloat64(b, c.SellVol)
	b = appendFloat64(b, c.Delta)
	b = appendFloat64(b, c.AvgScore)
	return b
}

func appendOrderbookSnapshot(b []byte, o *OrderbookSnapshot) []byte {
	b = append(b, 0x95)
	b = appendFloat64(b, o.BestBid)
	b = appendFloat64(b, o.BestAsk)
	b = appendFloat64(b, o.Spread)
	b = appendFloat64(b, o.Imbalance)
	b = appendInt64(b, int64(o.Score))
	return b
}

func appendOISnapshot(b []byte, o *OISnapshot) []byte {
	b = append(b, 0x94)
	b = appendFloat64(b, o.OI)
	b = appendFloat64(b, o.OIDelta1s)
	b = appendFloat64(b, o.OIDelta1m)
	b = appendInt64(b, int64(o.Behavior))
	return b
}

func appendFloat64(b []byte, v float64) []byte {
	b = append(b, 0xcb)
	bits := math.Float64bits(v)
	return append(b, byte(bits>>56), byte(bits>>48), byte(bits>>40), byte(bits>>32),
		byte(bits>>24), byte(bits>>16), byte(bits>>8), byte(bits))
}
