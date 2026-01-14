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
	name string
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

	players    [2]*client
	spectators map[string]*client

	paddleY [2]float64
	score   [2]int

	ballX  float64
	ballY  float64
	ballVX float64
	ballVY float64

	startTime time.Time
	endTime   time.Time
	lastTick  time.Time
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

type wsInJoin struct {
	RoomID string `json:"roomId"`
	Name   string `json:"name"`
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

	SecondsLeft int      `json:"secondsLeft"`
	Spectators  []string `json:"spectators"`
}

func newHub() *hub {
	return &hub{rooms: make(map[string]*room)}
}

func (h *hub) joinByRoomID(c *client, roomID string) bool {
	h.mu.Lock()
	r := h.rooms[roomID]
	h.mu.Unlock()
	if r == nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.spectators == nil {
		r.spectators = make(map[string]*client)
	}
	c.room = r
	c.side = -1
	r.spectators[c.id] = c
	return true
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
	// Remove from waiting queue.
	for i := range h.waitQ {
		if h.waitQ[i] == c {
			h.waitQ = append(h.waitQ[:i], h.waitQ[i+1:]...)
			h.mu.Unlock()
			return
		}
	}
	if c.room == nil {
		h.mu.Unlock()
		return
	}
	r := c.room
	h.mu.Unlock()

	r.mu.Lock()
	for side := 0; side < 2; side++ {
		if r.players[side] == c {
			r.players[side] = nil
		}
	}
	delete(r.spectators, c.id)
	empty := r.players[0] == nil && r.players[1] == nil && len(r.spectators) == 0
	r.mu.Unlock()

	if empty {
		h.mu.Lock()
		delete(h.rooms, r.id)
		h.mu.Unlock()
	}
}

const matchDuration = 5 * time.Minute

func newRoom(n int) *room {
	r := &room{
		id:         "room-" + itoa(n),
		spectators: make(map[string]*client),
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

	now := time.Now()
	r.lastTick = now
	if r.startTime.IsZero() {
		r.startTime = now
		r.endTime = now.Add(matchDuration)
	}
}

func (r *room) step(dt float64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	running := r.players[0] != nil && r.players[1] != nil
	if !running {
		return
	}
	if !r.endTime.IsZero() && time.Now().After(r.endTime) {
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
	leftFaceX := float64(paddleMargin + paddleW)
	rightFaceX := float64(worldW - paddleMargin - paddleW)
	leftPaddleX := float64(paddleMargin)
	rightPaddleX := float64(worldW - paddleMargin - paddleW)

	// Left paddle overlap.
	if r.ballVX < 0 && r.ballX-ballRadius <= leftFaceX {
		py := r.paddleY[0]
		if r.ballY >= py && r.ballY <= py+paddleH && r.ballX+ballRadius >= leftPaddleX {
			r.ballX = leftFaceX + ballRadius
			r.bounceOffPaddle(0)
		}
	}
	// Right paddle overlap.
	if r.ballVX > 0 && r.ballX+ballRadius >= rightFaceX {
		py := r.paddleY[1]
		if r.ballY >= py && r.ballY <= py+paddleH && r.ballX-ballRadius <= rightPaddleX+paddleW {
			r.ballX = rightFaceX - ballRadius
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

	// Flip direction and apply spin while preserving speed.
	dir := 1.0
	if side == 0 {
		dir = 1
	} else {
		dir = -1
	}
	if r.ballVX < 0 {
		dir = 1
	} else {
		dir = -1
	}
	vx := math.Abs(speed * math.Cos(angle))
	r.ballVX = dir * vx
	r.ballVY = speed * math.Sin(angle)
}

func (r *room) snapshot() wsOutState {
	r.mu.Lock()
	defer r.mu.Unlock()

	secondsLeft := 0
	if !r.endTime.IsZero() {
		secondsLeft = int(time.Until(r.endTime).Seconds())
		if secondsLeft < 0 {
			secondsLeft = 0
		}
	}

	spectators := make([]string, 0, len(r.spectators))
	for _, c := range r.spectators {
		if c == nil {
			continue
		}
		if c.name != "" {
			spectators = append(spectators, c.name)
		} else {
			spectators = append(spectators, c.id)
		}
	}

	running := r.players[0] != nil && r.players[1] != nil
	if !r.endTime.IsZero() && time.Now().After(r.endTime) {
		running = false
	}

	return wsOutState{
		PaddleY:     r.paddleY,
		BallX:       r.ballX,
		BallY:       r.ballY,
		Score:       r.score,
		Running:     running,
		SecondsLeft: secondsLeft,
		Spectators:  spectators,
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
