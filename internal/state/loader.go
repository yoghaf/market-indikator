package state

import (
	"bufio"
	"encoding/csv"
	"io"
	"log"
	"market-indikator/internal/model"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// LoadFromCSV reads the latest CSV log file and returns up to `limit`
// snapshots (most recent). Used ONLY when ring buffer is empty (restart).
//
// CSV header:
//   timestamp,price,final_score,
//   score_1s,score_1m,score_5m,score_15m,score_1h,
//   htf_bias,market_state,action_hint,
//   delta_1s,cvd,ob_score,oi,oi_delta,
//   behavior,event_flags
func LoadFromCSV(logDir string, limit int) []model.Snapshot {
	// Find latest CSV file
	pattern := filepath.Join(logDir, "*.csv")
	files, err := filepath.Glob(pattern)
	if err != nil || len(files) == 0 {
		log.Printf("[Loader] No CSV files found in %s", logDir)
		return nil
	}

	// Sort by name (YYYY-MM-DD.csv) → latest is last
	sort.Strings(files)
	latest := files[len(files)-1]
	log.Printf("[Loader] Loading history from %s", latest)

	f, err := os.Open(latest)
	if err != nil {
		log.Printf("[Loader] Failed to open %s: %v", latest, err)
		return nil
	}
	defer f.Close()

	// Read all rows (tail-read: we need the last N rows)
	reader := csv.NewReader(bufio.NewReaderSize(f, 1<<20)) // 1MB buffer
	reader.FieldsPerRecord = -1                             // flexible

	// Skip header
	header, err := reader.Read()
	if err != nil {
		log.Printf("[Loader] Failed to read header: %v", err)
		return nil
	}

	// Build column index map for safety
	idx := make(map[string]int)
	for i, h := range header {
		idx[strings.TrimSpace(h)] = i
	}

	var rows [][]string
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue // skip malformed
		}
		rows = append(rows, row)
	}

	// Take only the last `limit` rows
	if len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}

	log.Printf("[Loader] Parsed %d rows from CSV", len(rows))

	snapshots := make([]model.Snapshot, 0, len(rows))
	for _, row := range rows {
		snap := csvRowToSnapshot(row, idx)
		if snap.Time > 0 {
			snapshots = append(snapshots, snap)
		}
	}

	return snapshots
}

// csvRowToSnapshot converts a CSV row to a model.Snapshot.
// Since CSV doesn't have OHLC, we use Price for Open/High/Low/Close.
// This is a best-effort reconstruction for restart recovery.
func csvRowToSnapshot(row []string, idx map[string]int) model.Snapshot {
	get := func(col string) float64 {
		i, ok := idx[col]
		if !ok || i >= len(row) {
			return 0
		}
		v, _ := strconv.ParseFloat(strings.TrimSpace(row[i]), 64)
		return v
	}
	getInt := func(col string) int {
		i, ok := idx[col]
		if !ok || i >= len(row) {
			return 0
		}
		v, _ := strconv.Atoi(strings.TrimSpace(row[i]))
		return v
	}
	getInt64 := func(col string) int64 {
		i, ok := idx[col]
		if !ok || i >= len(row) {
			return 0
		}
		v, _ := strconv.ParseInt(strings.TrimSpace(row[i]), 10, 64)
		return v
	}

	ts := getInt64("timestamp")
	tsSec := ts / 1000 // CSV stores ms, engine uses seconds
	price := get("price")
	score := get("final_score")
	delta := get("delta_1s")
	cvd := get("cvd")
	oi := get("oi")
	oiDelta := get("oi_delta")
	behavior := getInt("behavior")
	obScore := getInt("ob_score")

	// Reconstruct candle from price (best-effort O=H=L=C=Price)
	candle1s := model.CandleSnapshot{
		Time:     tsSec,
		Open:     price,
		High:     price,
		Low:      price,
		Close:    price,
		Delta:    delta,
		AvgScore: get("score_1s"),
	}

	candle1m := model.CandleSnapshot{
		Time:     tsSec / 60 * 60, // align to minute boundary
		Open:     price,
		High:     price,
		Low:      price,
		Close:    price,
		AvgScore: get("score_1m"),
	}

	// Reconstruct HTF scores
	var htf [model.NumHTF]model.CandleSnapshot
	htf[0] = model.CandleSnapshot{Time: tsSec / 300 * 300, Close: price, AvgScore: get("score_5m")}
	htf[1] = model.CandleSnapshot{Time: tsSec / 900 * 900, Close: price, AvgScore: get("score_15m")}
	htf[2] = model.CandleSnapshot{Time: tsSec / 3600 * 3600, Close: price, AvgScore: get("score_1h")}
	// HTF 4h and 1d not in CSV — leave zero (acceptable for fallback)

	return model.Snapshot{
		Price:      price,
		Time:       tsSec,
		CVD:        cvd,
		Candle1s:   candle1s,
		Candle1m:   candle1m,
		Orderbook:  model.OrderbookSnapshot{Score: obScore},
		OI:         model.OISnapshot{OI: oi, OIDelta1m: oiDelta, Behavior: behavior},
		FinalScore: score,
		HTF:        htf,
	}
}
