package dashboard

import (
	"fmt"
	"net/http"
	"sync"
)

// WebSocketHub manages all active WebSocket connections.
type WebSocketHub struct {
	clients    map[*wsClient]bool
	broadcast  chan []byte
	register   chan *wsClient
	unregister chan *wsClient
	mu         sync.RWMutex
}

type wsClient struct {
	hub  *WebSocketHub
	send chan []byte
	conn http.ResponseWriter
}

func newWebSocketHub() *WebSocketHub {
	return &WebSocketHub{
		clients:    make(map[*wsClient]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *wsClient),
		unregister: make(chan *wsClient),
	}
}

func (h *WebSocketHub) run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

// broadcast sends a message to all connected clients.
func (h *WebSocketHub) broadcastMsg(message []byte) {
	select {
	case h.broadcast <- message:
	default:
	}
}

func (h *WebSocketHub) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Use Server-Sent Events as a lightweight alternative to WebSocket
	// (no external dependencies required)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	client := &wsClient{
		hub:  h,
		send: make(chan []byte, 256),
		conn: w,
	}

	h.register <- client
	defer func() { h.unregister <- client }()

	// Send initial connection message
	fmt.Fprintf(w, "data: {\"type\":\"connected\",\"message\":\"PicoClaw Dashboard connected\"}\n\n")
	flusher.Flush()

	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			return
		case msg, ok := <-client.send:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

// broadcast is a convenience method on the hub.
func (h *WebSocketHub) broadcast(msg []byte) {
	h.broadcastMsg(msg)
}
