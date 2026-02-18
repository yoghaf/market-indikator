package ingest

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	oi "market-indikator/internal/oi"
)

const (
	// Binance Futures Open Interest endpoint.
	// Poll every 3 seconds — well within 1200 req/min rate limit.
	oiURL      = "https://fapi.binance.com/fapi/v1/openInterest?symbol=BTCUSDT"
	oiInterval = 3 * time.Second
)

// oiResponse matches Binance OI REST response.
type oiResponse struct {
	OpenInterest string `json:"openInterest"`
}

// OIPoller polls Binance for open interest and feeds data to the OI engine.
// Runs entirely OFF the hot path in its own goroutine.
type OIPoller struct {
	engine   *oi.Engine
	priceFn  func() float64 // returns latest price (lock-free read)
	client   *http.Client
}

// NewOIPoller creates a poller.
// priceFn should be a closure that returns the latest trade price.
func NewOIPoller(engine *oi.Engine, priceFn func() float64) *OIPoller {
	return &OIPoller{
		engine:  engine,
		priceFn: priceFn,
		client: &http.Client{
			Timeout: 2 * time.Second, // Never block beyond 2s
		},
	}
}

func (p *OIPoller) Start(ctx context.Context) {
	go p.loop(ctx)
}

func (p *OIPoller) loop(ctx context.Context) {
	// Initial poll
	p.poll()

	ticker := time.NewTicker(oiInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll()
		}
	}
}

func (p *OIPoller) poll() {
	resp, err := p.client.Get(oiURL)
	if err != nil {
		log.Printf("OI poll error: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("OI poll HTTP %d: %s", resp.StatusCode, string(body))
		return
	}

	var data oiResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf("OI decode error: %v", err)
		return
	}

	oiVal, err := strconv.ParseFloat(data.OpenInterest, 64)
	if err != nil {
		log.Printf("OI parse error: %v", err)
		return
	}

	// Read latest price via closure (lock-free)
	currentPrice := p.priceFn()

	// Update OI engine — computes deltas and behavior classification
	p.engine.Update(oiVal, currentPrice)
	log.Printf("OI updated: %.2f contracts (price: $%.2f)", oiVal, currentPrice)
}
