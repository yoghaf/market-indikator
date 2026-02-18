package ingest

import (
	"context"
	"log"
	"strconv"
	"time"

	"market-indikator/internal/bus"
	"market-indikator/internal/model"

	"github.com/gorilla/websocket"
)

const (
	binanceWSURL      = "wss://fstream.binance.com/ws/btcusdt@aggTrade"
	reconnectDelay    = 1 * time.Second
	maxReconnectDelay = 30 * time.Second
)

// aggTradeEvent matches the full JSON structure from Binance aggTrade stream.
// See: https://developers.binance.com/docs/derivatives/usds-margined-futures/websocket-market-streams/Aggregate-Trade-Streams
// Example: {"e":"aggTrade","E":1672515782136,"s":"BTCUSDT","a":123456789,"p":"16850.00","q":"0.005","f":100,"l":105,"T":1672515782136,"m":true}
type aggTradeEvent struct {
	EventType string `json:"e"` // Event type (always "aggTrade")
	E         int64  `json:"E"` // Event time
	Symbol    string `json:"s"` // Symbol
	A         int64  `json:"a"` // AggTradeID
	P         string `json:"p"` // Price
	Q         string `json:"q"` // Quantity
	F         int64  `json:"f"` // First trade ID
	L         int64  `json:"l"` // Last trade ID
	T         int64  `json:"T"` // Trade time
	M         bool   `json:"m"` // Is the buyer the market maker?
}

type Ingester struct {
	bus *bus.Bus
}

func NewIngester(b *bus.Bus) *Ingester {
	return &Ingester{
		bus: b,
	}
}

func (i *Ingester) Start(ctx context.Context) {
	go i.loop(ctx)
}

func (i *Ingester) loop(ctx context.Context) {
	delay := reconnectDelay

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := i.connectAndConsume(ctx)
		if err != nil {
			log.Printf("Ingest error: %v. Reconnecting in %v...", err, delay)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			delay *= 2
			if delay > maxReconnectDelay {
				delay = maxReconnectDelay
			}
		} else {
			// specific exit (e.g. graceful close) or unexpected nil
			delay = reconnectDelay
		}
	}
}

func (i *Ingester) connectAndConsume(ctx context.Context) error {
	c, _, err := websocket.DefaultDialer.Dial(binanceWSURL, nil)
	if err != nil {
		return err
	}
	defer c.Close()

	log.Println("Connected to Binance Futures WebSocket")

	// Pre-allocate for parsing
	var event aggTradeEvent

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// Read message
		// Using ReadJSON for simplicity now, but ReadMessage + custom parsing is lower latency
		// internal processing targets <10ms, ReadJSON is usually <0.1ms for this size, so acceptable for MVP.
		err := c.ReadJSON(&event)
		if err != nil {
			return err
		}

		// Parse strings to float
		// Optimization: fastfloat or similar would be better, but ParseFloat is robust.
		price, _ := strconv.ParseFloat(event.P, 64)
		qty, _ := strconv.ParseFloat(event.Q, 64)

		trade := model.Trade{
			ID:       event.A, // Using aggTradeID as ID
			Price:    price,
			Quantity: qty,
			Time:     event.T,
			IsBuyer:  event.M, // In aggTrade, 'm' means buyer is maker (so it was a Sell order that filled)
		}

		// Publish to internal bus
		i.bus.Publish(trade)
	}
}
