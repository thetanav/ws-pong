package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var globalHub = newHub()

var nextClientID atomic.Int64

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		return
	}

	c := &client{
		id:   fmt.Sprintf("c-%d", nextClientID.Add(1)),
		conn: conn,
		send: make(chan []byte, 64),
		side: -1,
	}
	c.mouseY.Store(-1)

	globalHub.assignToRoom(c)

	// Welcome message.
	hello := wsOut{Type: "hello", Data: wsOutHello{ClientID: c.id, RoomID: roomID(c), Side: c.side, W: worldW, H: worldH}}
	b, _ := json.Marshal(hello)
	c.send <- b

	go writePump(c)
	readPump(c)
}

func roomID(c *client) string {
	if c.room == nil {
		return ""
	}
	return c.room.id
}

func readPump(c *client) {
	defer func() {
		globalHub.removeClient(c)
		close(c.send)
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(1 << 20)
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		var msg wsIn
		if err := c.conn.ReadJSON(&msg); err != nil {
			return
		}

		switch msg.Type {
		case "move":
			var m wsInMove
			if err := json.Unmarshal(msg.Data, &m); err != nil {
				continue
			}
			if m.Dir < -1 {
				m.Dir = -1
			}
			if m.Dir > 1 {
				m.Dir = 1
			}
			c.moveDir.Store(int32(m.Dir))
			c.mouseY.Store(-1)
		case "mouse":
			var m wsInMouse
			if err := json.Unmarshal(msg.Data, &m); err != nil {
				continue
			}
			c.mouseY.Store(int32(m.Y))
			c.moveDir.Store(0)
		}
	}
}

func writePump(c *client) {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, "./web/index.html")
}

func main() {
	go runLoop(globalHub)

	http.HandleFunc("/", handleIndex)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./web/static"))))
	http.HandleFunc("/ws", handleWS)

	log.Println("Pong server running on http://localhost:8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

func runLoop(h *hub) {
	ticker := time.NewTicker(time.Second / tickRate)
	defer ticker.Stop()

	for range ticker.C {
		h.mu.Lock()
		rooms := make([]*room, 0, len(h.rooms))
		for _, r := range h.rooms {
			rooms = append(rooms, r)
		}
		h.mu.Unlock()

		dt := 1.0 / float64(tickRate)
		for _, r := range rooms {
			r.step(dt)
			state := r.snapshot()
			payload, _ := json.Marshal(wsOut{Type: "state", Data: state})
			for side := 0; side < 2; side++ {
				p := r.players[side]
				if p == nil {
					continue
				}
				select {
				case p.send <- payload:
				default:
					// Drop if slow; connection will timeout eventually.
				}
			}
		}
	}
}
