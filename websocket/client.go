package websocket

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 512 * 1024 // 512 KB
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all CORS for development
	},
}

type Client struct {
	Hub       *Hub
	Conn      *websocket.Conn
	send      chan []byte
	closeOnce sync.Once // Guards close(send) so it's only called once
	UserID    int
	UserName  string // Cached at connect time — avoids DB fetch on every ack_seen
	AvatarURL string // Cached at connect time
}

func (c *Client) ReadPump() {
	defer func() {
		c.Hub.unregister <- c
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(maxMessageSize)
	c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Socket error: %v", err)
			}
			break
		}
		// Event Hub-e pathano
		c.Hub.RouteIncomingEvent(c, message)
	}
}

func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)

	defer func() {
		ticker.Stop()
		c.Conn.Close()
		c.Hub.unregister <- c
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))

			if !ok {
				c.Conn.WriteMessage(
					websocket.CloseMessage,
					[]byte{},
				)
				return
			}

			// First queued event.
			messages := [][]byte{message}

			// Collect events that are already waiting in the queue.
			n := len(c.send)

			for i := 0; i < n; i++ {
				messages = append(messages, <-c.send)
			}

			// Convert:
			//
			// event
			// event
			// event
			//
			// into:
			//
			// [event, event, event]
			batch := make([]json.RawMessage, 0, len(messages))

			for _, message := range messages {
				batch = append(batch, json.RawMessage(message))
			}

			data, err := json.Marshal(batch)
			if err != nil {
				log.Printf(
					"[WRITE_PUMP] failed to marshal batch: %v",
					err,
				)
				continue
			}

			log.Printf(
				"[WRITE_PUMP] user=%d batch_size=%d sending=%s",
				c.UserID,
				len(batch),
				string(data),
			)

			if err := c.Conn.WriteMessage(
				websocket.TextMessage,
				data,
			); err != nil {
				return
			}

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))

			if err := c.Conn.WriteMessage(
				websocket.PingMessage,
				nil,
			); err != nil {
				return
			}
		}
	}
}

func ServeWs(hub *Hub, w http.ResponseWriter, r *http.Request, userID int, userName string, avatarURL string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade failed:", err)
		return
	}

	client := &Client{
		Hub:       hub,
		Conn:      conn,
		send:      make(chan []byte, 512), // Increased from 256 to handle large groups
		UserID:    userID,
		UserName:  userName,
		AvatarURL: avatarURL,
	}
	client.Hub.register <- client

	// Start two lightweight Goroutines
	go client.WritePump()
	go client.ReadPump()
}
