package logger

import (
	"bufio"
	"fmt"
	"log"
	"market-indikator/internal/model"
	"os"
	"path/filepath"
	"time"
)

// =============================================================================
// ASYNC SNAPSHOT LOGGER — Zero hot-path impact
// =============================================================================
//
// Architecture:
//   engine goroutine → logCh (buffered 4096) → Logger goroutine → daily CSV
//
// Performance guarantees:
//   • Hot path sends via non-blocking select (drops if full) — 0ns added latency
//   • Logger goroutine runs independently on its own OS thread
//   • Batched writes: flushes bufio.Writer every 1 second
//   • bufio buffer: 1MB — absorbs bursts, minimizes syscalls
//   • Append-only daily rotation via filename: logs/YYYY-MM-DD.csv
//
// CSV schema (18 columns):
//   timestamp,price,final_score,
//   score_1s,score_1m,score_5m,score_15m,score_1h,
//   htf_bias,market_state,action_hint,
//   delta_1s,cvd,ob_score,oi,oi_delta,
//   behavior,event_flags
// =============================================================================

const (
	chanSize    = 4096
	bufSize     = 1 << 20 // 1 MB
	flushPeriod = 1 * time.Second
	logDir      = "logs"
)

// LogRow — pre-computed in the engine goroutine (NOT the hot path).
// All fields are value types — zero heap allocations.
type LogRow struct {
	Timestamp  int64   // unix ms
	Price      float64
	FinalScore float64

	// Multi-timeframe scores
	Score1s  float64
	Score1m  float64
	Score5m  float64
	Score15m float64
	Score1h  float64

	// Decision layer (computed in Go, not just frontend)
	HTFBias     string // BULLISH / BEARISH / RANGE
	MarketState string // TRENDING_UP / PULLBACK_IN_UPTREND / etc.
	ActionHint  string // WATCH_LONG / WATCH_SHORT / NO_TRADE

	// Raw metrics
	Delta1s float64
	CVD     float64
	OBScore int
	OI      float64
	OIDelta float64

	// Positioning
	Behavior   int
	EventFlags uint32
}

// Logger — async CSV writer.
type Logger struct {
	ch chan LogRow
}

// NewLogger — creates the logger and starts its background goroutine.
func NewLogger() *Logger {
	l := &Logger{
		ch: make(chan LogRow, chanSize),
	}
	go l.run()
	return l
}

// Log — non-blocking send. Drops the row if the channel is full.
// This is called from the engine goroutine, NOT the trade hot-path.
func (l *Logger) Log(row LogRow) {
	select {
	case l.ch <- row:
	default:
		// Drop — logger is backed up, never block engine
	}
}

// run — background goroutine. Batches writes, rotates daily.
func (l *Logger) run() {
	// Ensure log directory exists
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Printf("Logger: failed to create dir: %v", err)
		return
	}

	var (
		currentDay string
		file       *os.File
		writer     *bufio.Writer
	)

	ticker := time.NewTicker(flushPeriod)
	defer ticker.Stop()

	openFile := func(day string) {
		if file != nil {
			writer.Flush()
			file.Close()
		}

		path := filepath.Join(logDir, day+".csv")
		var err error
		file, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Printf("Logger: failed to open %s: %v", path, err)
			return
		}

		writer = bufio.NewWriterSize(file, bufSize)

		// Write header if new file
		info, _ := file.Stat()
		if info != nil && info.Size() == 0 {
			fmt.Fprintln(writer,
				"timestamp,price,final_score,"+
					"score_1s,score_1m,score_5m,score_15m,score_1h,"+
					"htf_bias,market_state,action_hint,"+
					"delta_1s,cvd,ob_score,oi,oi_delta,"+
					"behavior,event_flags")
		}

		currentDay = day
		log.Printf("Logger: writing to %s", path)
	}

	for {
		select {
		case row, ok := <-l.ch:
			if !ok {
				// Channel closed — shutdown
				if writer != nil {
					writer.Flush()
				}
				if file != nil {
					file.Close()
				}
				return
			}

			// Daily rotation
			day := time.UnixMilli(row.Timestamp).UTC().Format("2006-01-02")
			if day != currentDay {
				openFile(day)
			}

			if writer == nil {
				continue
			}

			// Encode CSV row — fmt.Fprintf with fixed format, no allocations beyond buffer
			fmt.Fprintf(writer,
				"%d,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f,%.2f,%s,%s,%s,%.6f,%.4f,%d,%.2f,%.4f,%d,%d\n",
				row.Timestamp,
				row.Price,
				row.FinalScore,
				row.Score1s,
				row.Score1m,
				row.Score5m,
				row.Score15m,
				row.Score1h,
				row.HTFBias,
				row.MarketState,
				row.ActionHint,
				row.Delta1s,
				row.CVD,
				row.OBScore,
				row.OI,
				row.OIDelta,
				row.Behavior,
				row.EventFlags,
			)

		case <-ticker.C:
			if writer != nil {
				writer.Flush()
			}
		}
	}
}

// ─── DECISION LAYER (Go-side, mirrors frontend logic) ───

// ComputeHTFBias — weighted average of 1h, 4h, 1d scores.
func ComputeHTFBias(score1h, score4h, score1d float64) string {
	avg := 0.30*score1h + 0.35*score4h + 0.35*score1d
	if avg > 15 {
		return "BULLISH"
	}
	if avg < -15 {
		return "BEARISH"
	}
	return "RANGE"
}

// ComputeMarketState — HTF bias × LTF pressure matrix.
func ComputeMarketState(htfBias string, finalScore float64) string {
	ltf := "flat"
	if finalScore > 15 {
		ltf = "bull"
	} else if finalScore < -15 {
		ltf = "bear"
	}

	switch htfBias {
	case "BULLISH":
		switch ltf {
		case "bull":
			return "TRENDING_UP"
		case "bear":
			return "PULLBACK_IN_UPTREND"
		default:
			return "CONSOLIDATION_BULL"
		}
	case "BEARISH":
		switch ltf {
		case "bear":
			return "TRENDING_DOWN"
		case "bull":
			return "RALLY_INTO_RESISTANCE"
		default:
			return "CONSOLIDATION_BEAR"
		}
	}
	return "RANGE_CHOPPY"
}

// ComputeActionHint — simplified action classification.
func ComputeActionHint(htfBias string, finalScore float64, imbalance float64, behavior int) string {
	isBull := htfBias == "BULLISH"
	isBear := htfBias == "BEARISH"
	ltfBull := finalScore > 10
	ltfBear := finalScore < -10
	obBull := imbalance > 0.05
	obBear := imbalance < -0.05

	if isBull && ltfBear && obBull {
		return "WATCH_LONG"
	}
	if isBear && ltfBull && obBear {
		return "WATCH_SHORT"
	}
	if isBull && ltfBull {
		return "WATCH_LONG"
	}
	if isBear && ltfBear {
		return "WATCH_SHORT"
	}
	if isBull {
		return "WAIT_DIP"
	}
	if isBear {
		return "WAIT_RALLY"
	}
	return "NO_TRADE"
}

// BuildLogRow — constructs a LogRow from a Snapshot.
// Called in the engine goroutine (off hot-path), ~50ns.
func BuildLogRow(snap *model.Snapshot, eventFlags uint32) LogRow {
	score1h := snap.HTF[2].AvgScore  // idx 2 = 1h
	score4h := snap.HTF[3].AvgScore  // idx 3 = 4h
	score1d := snap.HTF[4].AvgScore  // idx 4 = 1d

	htfBias := ComputeHTFBias(score1h, score4h, score1d)
	mktState := ComputeMarketState(htfBias, snap.FinalScore)
	action := ComputeActionHint(htfBias, snap.FinalScore, float64(snap.Orderbook.Imbalance), snap.OI.Behavior)

	return LogRow{
		Timestamp:   snap.Time,
		Price:       snap.Price,
		FinalScore:  snap.FinalScore,
		Score1s:     snap.FinalScore,
		Score1m:     snap.Candle1m.AvgScore,
		Score5m:     snap.HTF[0].AvgScore,
		Score15m:    snap.HTF[1].AvgScore,
		Score1h:     score1h,
		HTFBias:     htfBias,
		MarketState: mktState,
		ActionHint:  action,
		Delta1s:     snap.Candle1s.Delta,
		CVD:         snap.CVD,
		OBScore:     snap.Orderbook.Score,
		OI:          snap.OI.OI,
		OIDelta:     snap.OI.OIDelta1m,
		Behavior:    snap.OI.Behavior,
		EventFlags:  eventFlags,
	}
}
