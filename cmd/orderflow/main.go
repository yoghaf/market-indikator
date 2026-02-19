package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"market-indikator/internal/broadcast"
	"market-indikator/internal/bus"
	"market-indikator/internal/engine"
	"market-indikator/internal/ingest"
	csvlogger "market-indikator/internal/logger"
	"market-indikator/internal/model"
	oi "market-indikator/internal/oi"
	"market-indikator/internal/orderbook"
	"market-indikator/internal/state"
)

const (
	bufferSize = 3600 // 1 hour of 1s snapshots
	logDir     = "logs"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting Market Indikator v6 (Stateful Snapshot Engine)...")

	ctx, cancel := context.WithCancel(context.Background())

	// 1. Trade Bus
	eventBus := bus.NewBus()

	// 2. Orderbook
	book := orderbook.NewBook()

	// 3. OI Engine
	oiEngine := oi.NewEngine()

	// 4. Trade Engine (merges all analytics)
	eng := engine.NewEngine(book, oiEngine)

	// 5. Snapshot Logger (async, zero hot-path impact)
	snapLogger := csvlogger.NewLogger()

	// 6. Snapshot Ring Buffer (in-memory state for new clients)
	snapBuffer := state.NewRingBuffer(bufferSize)

	// 7. Load history from CSV on startup (restart recovery)
	csvSnapshots := state.LoadFromCSV(logDir, bufferSize)
	for _, snap := range csvSnapshots {
		snapBuffer.Add(snap)
	}
	log.Printf("Ring buffer pre-loaded with %d snapshots from CSV", snapBuffer.Size())

	// 8. Start Binance AggTrade Ingest
	ingester := ingest.NewIngester(eventBus)
	ingester.Start(ctx)

	// 9. Start Binance Depth Ingest
	depthIngester := ingest.NewDepthIngester(book)
	depthIngester.Start(ctx)

	// 10. Start OI Poller (reads latest price from engine via closure)
	oiPoller := ingest.NewOIPoller(oiEngine, eng.GetPrice)
	oiPoller.Start(ctx)

	// 11. Engine goroutine — single owner, no locks
	tradeCh := eventBus.Subscribe(1024)
	snapshotCh := make(chan model.Snapshot, 1024)

	go func() {
		var lastLogTime int64
		for trade := range tradeCh {
			snap := eng.ProcessTrade(trade)

			// Push to ring buffer (thread-safe)
			snapBuffer.Add(snap)

			// Broadcast to WebSocket clients (non-blocking)
			select {
			case snapshotCh <- snap:
			default:
			}

			// Log at most once per second (same candle time = same second)
			if snap.Candle1s.Time != lastLogTime {
				lastLogTime = snap.Candle1s.Time
				row := csvlogger.BuildLogRow(&snap, 0) // eventFlags=0 for now
				snapLogger.Log(row)
			}
		}
	}()

	// 12. Broadcaster (now with ring buffer for snapshot history)
	broadcaster := broadcast.NewBroadcaster(snapshotCh, snapBuffer)
	go broadcaster.Start(":8080")

	// 13. Shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	cancel()
}
