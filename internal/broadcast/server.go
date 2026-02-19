package broadcast

import (
	"log"
	"net/http"

	"market-indikator/internal/model"
	"market-indikator/internal/state"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for now
	},
}

// Broadcaster receives Snapshots from the engine and fans them out to WS clients.
type Broadcaster struct {
	input  <-chan model.Snapshot
	buffer *state.RingBuffer
}

func NewBroadcaster(input <-chan model.Snapshot, buffer *state.RingBuffer) *Broadcaster {
	return &Broadcaster{input: input, buffer: buffer}
}

// Start launches the broadcast loop and HTTP server.
func (b *Broadcaster) Start(addr string) {
	hub := newHub(b.buffer)
	go hub.run(b.input)

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(hub, w, r)
	})

	log.Printf("Broadcaster listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}

// Hub maintains active clients and broadcasts MsgPack messages to all.
type Hub struct {
	clients    map[*Client]bool
	register   chan *Client
	unregister chan *Client
	buffer     *state.RingBuffer
}

func newHub(buffer *state.RingBuffer) *Hub {
	return &Hub{
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
		buffer:     buffer,
	}
}

func (h *Hub) run(input <-chan model.Snapshot) {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
			log.Printf("Client connected (%d total)", len(h.clients))
		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				log.Printf("Client disconnected (%d total)", len(h.clients))
			}
		case snap := <-input:
			// Serialize ONCE per snapshot.
			msg := snap.AppendMsgPack(make([]byte, 0, 128))

			// Fan-out to all connected clients.
			for client := range h.clients {
				select {
				case client.send <- msg:
				default:
					// Slow client — drop this tick, don't kill.
					// Client will catch up on next tick.
					// Dead clients are cleaned up via readPump.
				}
			}
		}
	}
}

type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// ═══════════════════════════════════════════════════════════════
// STREAMING HISTORY PROTOCOL
// ═══════════════════════════════════════════════════════════════
//
// Instead of sending one giant MsgPack array (which blocks JS decode),
// we stream history as individual small messages:
//
//   Message 1: MsgPack uint32 = count of history snapshots
//   Message 2..N+1: Individual FixArray(9) snapshots (~128 bytes each)
//   After: Client registered for live FixArray(9) ticks
//
// Frontend detects the header (typeof decoded === 'number') and
// shows a loading progress bar until all history snapshots arrive.
// Each individual message decodes in <0.1ms — zero main thread blocking.

func serveWs(hub *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	client := &Client{hub: hub, conn: conn, send: make(chan []byte, 4096)}

	// Send full history BEFORE registering for live ticks
	if hub.buffer != nil {
		snapshots := hub.buffer.GetAll()
		if len(snapshots) > 0 {
			// 1. Send count header (MsgPack uint32: 0xce + 4 bytes big-endian)
			n := uint32(len(snapshots))
			header := []byte{0xce, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
			if err := conn.WriteMessage(websocket.BinaryMessage, header); err != nil {
				log.Printf("Failed to send history header: %v", err)
				conn.Close()
				return
			}

			// 2. Stream each snapshot as individual message
			for _, snap := range snapshots {
				msg := snap.AppendMsgPack(make([]byte, 0, 128))
				if err := conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
					log.Printf("History stream interrupted after %d snapshots: %v", n, err)
					conn.Close()
					return
				}
			}
			log.Printf("Streamed %d history snapshots to new client", len(snapshots))
		}
	}

	// Register for live ticks
	client.hub.register <- client

	go client.writePump()
	go client.readPump()
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (c *Client) writePump() {
	defer func() {
		c.conn.Close()
	}()
	for {
		message, ok := <-c.send
		if !ok {
			c.conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
		}

		w, err := c.conn.NextWriter(websocket.BinaryMessage)
		if err != nil {
			return
		}
		w.Write(message)

		if err := w.Close(); err != nil {
			return
		}
	}
}
