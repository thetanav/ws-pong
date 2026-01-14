package main

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	worldW         = 800
	worldH         = 600
	paddleW        = 12
	paddleH        = 90
	ballRadius     = 8
	paddleMargin   = 20
	paddleSpeedPxS = 420
	ballBaseSpeed  = 360
	maxBallSpeed   = 850
	tickRate       = 60
)

type client struct {
	id   string
	conn *websocket.Conn
	send chan []byte

	room *room
	side int // 0 left, 1 right, -1 spectator

	// input state
	moveDir atomic.Int32 // -1,0,1
	mouseY  atomic.Int32 // -1 means unused
}

type room struct {
	id string
	mu sync.Mutex

	players [2]*client

	paddleY [2]float64
	score   [2]int

	ballX  float64
	ballY  float64
	ballVX float64
	ballVY float64

	lastTick time.Time
}

type hub struct {
	mu      sync.Mutex
	waitQ   []*client
	nextRID int
	rooms   map[string]*room
}

type wsIn struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

type wsInMove struct {
	Dir int `json:"dir"` // -1 up, 1 down, 0 stop
}

type wsInMouse struct {
	Y float64 `json:"y"` // canvas-relative y
}

type wsOut struct {
	Type string      `json:"type"`
	Data interface{} `json:"data,omitempty"`
}

type wsOutHello struct {
	ClientID string `json:"clientId"`
	RoomID   string `json:"roomId"`
	Side     int    `json:"side"` // 0 left, 1 right, -1 spectator
	W        int    `json:"w"`
	H        int    `json:"h"`
}

type wsOutState struct {
	PaddleY [2]float64 `json:"paddleY"`
	BallX   float64    `json:"ballX"`
	BallY   float64    `json:"ballY"`
	Score   [2]int     `json:"score"`
	Running bool       `json:"running"`
}

func newHub() *hub {
	return &hub{rooms: make(map[string]*room)}
}

func (h *hub) assignToRoom(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// If someone is waiting, pair them.
	if len(h.waitQ) > 0 {
		other := h.waitQ[0]
		h.waitQ = h.waitQ[1:]

		rid := h.nextRID
		h.nextRID++
		r := newRoom(rid)
		h.rooms[r.id] = r

		r.players[0] = other
		r.players[1] = c
		other.room, other.side = r, 0
		c.room, c.side = r, 1
		return
	}

	// Otherwise wait.
	h.waitQ = append(h.waitQ, c)
	c.side = -1
}

func (h *hub) removeClient(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Remove from waiting queue.
	for i := range h.waitQ {
		if h.waitQ[i] == c {
			h.waitQ = append(h.waitQ[:i], h.waitQ[i+1:]...)
			return
		}
	}

	if c.room == nil {
		return
	}

	r := c.room

	// Remove from room players.
	r.mu.Lock()
	for side := 0; side < 2; side++ {
		if r.players[side] == c {
			r.players[side] = nil
		}
	}
	running := r.players[0] != nil && r.players[1] != nil
	r.mu.Unlock()

	if !running {
		// Tear down room if a player left.
		delete(h.rooms, r.id)
	}
}

func newRoom(n int) *room {
	r := &room{
		id: "room-" + itoa(n),
	}
	r.resetRoundLocked()
	return r
}

func (r *room) resetRoundLocked() {
	r.paddleY[0] = (worldH - paddleH) / 2
	r.paddleY[1] = (worldH - paddleH) / 2

	r.ballX = worldW / 2
	r.ballY = worldH / 2

	angle := (rand.Float64()*0.8 - 0.4) // -0.4..0.4 radians-ish
	dir := 1.0
	if rand.IntN(2) == 0 {
		dir = -1
	}
	r.ballVX = dir * ballBaseSpeed
	r.ballVY = math.Tan(angle) * ballBaseSpeed
	r.lastTick = time.Now()
}

func (r *room) step(dt float64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	running := r.players[0] != nil && r.players[1] != nil
	if !running {
		return
	}

	// Apply paddle movement.
	for side := 0; side < 2; side++ {
		p := r.players[side]
		if p == nil {
			continue
		}
		if y := p.mouseY.Load(); y >= 0 {
			r.paddleY[side] = clamp(float64(y)-paddleH/2, 0, worldH-paddleH)
		} else {
			dir := float64(p.moveDir.Load())
			r.paddleY[side] = clamp(r.paddleY[side]+dir*paddleSpeedPxS*dt, 0, worldH-paddleH)
		}
	}

	// Move ball.
	r.ballX += r.ballVX * dt
	r.ballY += r.ballVY * dt

	// Wall bounce (top/bottom).
	if r.ballY-ballRadius < 0 {
		r.ballY = ballRadius
		r.ballVY *= -1
	}
	if r.ballY+ballRadius > worldH {
		r.ballY = worldH - ballRadius
		r.ballVY *= -1
	}

	// Paddle collisions.
	leftX := float64(paddleMargin + paddleW)
	rightX := float64(worldW - paddleMargin - paddleW)

	if r.ballVX < 0 && r.ballX-ballRadius <= leftX {
		py := r.paddleY[0]
		if r.ballY >= py && r.ballY <= py+paddleH {
			r.ballX = leftX + ballRadius
			// r.ballVY *= -0.98
			// r.ballVX *= -1
			r.bounceOffPaddle(0)
		}
	}
	if r.ballVX > 0 && r.ballX+ballRadius >= rightX {
		py := r.paddleY[1]
		if r.ballY >= py && r.ballY <= py+paddleH {
			r.ballX = rightX - ballRadius
			// r.ballVY *= -1.02
			// r.ballVX *= -1
			r.bounceOffPaddle(1)
		}
	}

	// Scoring.
	if r.ballX+ballRadius < 0 {
		r.score[1]++
		r.resetRoundLocked()
	}
	if r.ballX-ballRadius > worldW {
		r.score[0]++
		r.resetRoundLocked()
	}
}

func (r *room) bounceOffPaddle(side int) {
	// Add spin based on hit position.
	p := r.paddleY[side]
	rel := (r.ballY - (p + paddleH/2)) / (paddleH / 2) // -1..1
	rel = clamp(rel, -1, 1)

	speed := math.Hypot(r.ballVX, r.ballVY)
	speed = clamp(speed*1.04, ballBaseSpeed, maxBallSpeed)

	angle := rel * 0.9 // max ~50 degrees

	r.ballVX *= -1
	r.ballVY = speed * math.Sin(angle)
}

func (r *room) snapshot() wsOutState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return wsOutState{
		PaddleY: r.paddleY,
		BallX:   r.ballX,
		BallY:   r.ballY,
		Score:   r.score,
		Running: r.players[0] != nil && r.players[1] != nil,
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [32]byte{}
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
