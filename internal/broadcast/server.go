package broadcast

import (
	"log"
	"net/http"

	"market-indikator/internal/model"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for now
	},
}

// Broadcaster receives Snapshots from the engine and fans them out to WS clients.
type Broadcaster struct {
	input <-chan model.Snapshot
}

func NewBroadcaster(input <-chan model.Snapshot) *Broadcaster {
	return &Broadcaster{input: input}
}

// Start launches the broadcast loop and HTTP server.
func (b *Broadcaster) Start(addr string) {
	hub := newHub()
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
}

func newHub() *Hub {
	return &Hub{
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
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
			// 128 bytes is enough for the full enriched snapshot.
			msg := snap.AppendMsgPack(make([]byte, 0, 128))

			// Fan-out to all connected clients.
			for client := range h.clients {
				select {
				case client.send <- msg:
				default:
					// Slow client — drop to preserve system latency
					close(client.send)
					delete(h.clients, client)
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

func serveWs(hub *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	client := &Client{hub: hub, conn: conn, send: make(chan []byte, 256)}
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
