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
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting Market Indikator v5 (CVD + Delta + Orderbook + OI + Logger)...")

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

	// 6. Start Binance AggTrade Ingest
	ingester := ingest.NewIngester(eventBus)
	ingester.Start(ctx)

	// 7. Start Binance Depth Ingest
	depthIngester := ingest.NewDepthIngester(book)
	depthIngester.Start(ctx)

	// 8. Start OI Poller (reads latest price from engine via closure)
	oiPoller := ingest.NewOIPoller(oiEngine, eng.GetPrice)
	oiPoller.Start(ctx)

	// 9. Engine goroutine — single owner, no locks
	tradeCh := eventBus.Subscribe(1024)
	snapshotCh := make(chan model.Snapshot, 1024)

	go func() {
		var lastLogTime int64
		for trade := range tradeCh {
			snap := eng.ProcessTrade(trade)

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

	// 10. Broadcaster
	broadcaster := broadcast.NewBroadcaster(snapshotCh)
	go broadcaster.Start(":8080")

	// 11. Shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	cancel()
}
