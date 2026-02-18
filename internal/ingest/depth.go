package ingest

import (
	"context"
	"log"
	"strconv"
	"time"

	"market-indikator/internal/orderbook"

	"github.com/gorilla/websocket"
)

const (
	// Partial book depth stream: top 20 levels, 100ms updates
	// This gives us a full snapshot every 100ms — no need for diff management.
	depthWSURL      = "wss://fstream.binance.com/ws/btcusdt@depth20@100ms"
	depthReconnect  = 1 * time.Second
	depthMaxReconn  = 30 * time.Second
)

// depthEvent matches Binance partial depth stream JSON.
// Example: {"lastUpdateId":123456,"E":1672515782136,"T":1672515782100,"bids":[["16850.00","1.5"],...],"asks":[["16851.00","0.8"],...]}
type depthEvent struct {
	Bids [][]string `json:"bids"`
	Asks [][]string `json:"asks"`
}

// DepthIngester connects to Binance depth stream and updates the orderbook.
type DepthIngester struct {
	book *orderbook.Book
}

func NewDepthIngester(book *orderbook.Book) *DepthIngester {
	return &DepthIngester{book: book}
}

func (d *DepthIngester) Start(ctx context.Context) {
	go d.loop(ctx)
}

func (d *DepthIngester) loop(ctx context.Context) {
	delay := depthReconnect

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := d.connectAndConsume(ctx)
		if err != nil {
			log.Printf("Depth ingest error: %v. Reconnecting in %v...", err, delay)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			delay *= 2
			if delay > depthMaxReconn {
				delay = depthMaxReconn
			}
		} else {
			delay = depthReconnect
		}
	}
}

func (d *DepthIngester) connectAndConsume(ctx context.Context) error {
	c, _, err := websocket.DefaultDialer.Dial(depthWSURL, nil)
	if err != nil {
		return err
	}
	defer c.Close()

	log.Println("Connected to Binance Depth Stream")

	// Pre-allocate parsing buffers to avoid per-message allocations.
	// These slices are reused across messages.
	bids := make([]orderbook.PriceLevel, 0, orderbook.MaxDepthLevels)
	asks := make([]orderbook.PriceLevel, 0, orderbook.MaxDepthLevels)
	var event depthEvent

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		err := c.ReadJSON(&event)
		if err != nil {
			return err
		}

		// Parse string pairs into PriceLevel structs.
		// Reuse slices to minimize allocations.
		bids = bids[:0]
		for _, lvl := range event.Bids {
			if len(lvl) < 2 {
				continue
			}
			price, _ := strconv.ParseFloat(lvl[0], 64)
			qty, _ := strconv.ParseFloat(lvl[1], 64)
			if qty > 0 {
				bids = append(bids, orderbook.PriceLevel{Price: price, Quantity: qty})
			}
		}

		asks = asks[:0]
		for _, lvl := range event.Asks {
			if len(lvl) < 2 {
				continue
			}
			price, _ := strconv.ParseFloat(lvl[0], 64)
			qty, _ := strconv.ParseFloat(lvl[1], 64)
			if qty > 0 {
				asks = append(asks, orderbook.PriceLevel{Price: price, Quantity: qty})
			}
		}

		// Update book — this computes all pressure metrics and publishes atomically.
		d.book.UpdateDepth(bids, asks)
	}
}
