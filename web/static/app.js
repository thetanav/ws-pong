(() => {
  const statusEl = document.getElementById('status')
  const canvas = document.getElementById('c')
  const ctx = canvas.getContext('2d')
  const keysEl = document.getElementById('keys')

  const state = {
    hello: null,
    game: {
      paddleY: [255, 255],
      ballX: 400,
      ballY: 300,
      score: [0, 0],
      running: false,
      secondsLeft: 0,
      spectators: [],
    },

    // For smoothing/interpolation.
    lastServerState: null,
    lastServerAt: 0,

    render: {
      ballX: 400,
      ballY: 300,
    },
  }

  function sideName(side) {
    if (side === 0) return 'Left (W/S)'
    if (side === 1) return 'Right (↑/↓)'
    return 'Spectating/Waiting…'
  }

  function wsURL() {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    return `${proto}://${location.host}/ws`
  }

  let ws

  function send(type, data) {
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    ws.send(JSON.stringify({ type, data }))
  }

  function getParams() {
    const p = new URLSearchParams(location.search)
    return {
      roomId: p.get('room') || '',
      name: p.get('name') || '',
    }
  }

  function connect() {
    ws = new WebSocket(wsURL())

    ws.onopen = () => {
      const { roomId, name } = getParams()
      if (roomId) {
        statusEl.textContent = 'Connected. Joining room…'
        send('join', { roomId, name })
      } else {
        if (name) send('name', { name })
        statusEl.textContent = 'Connected. Pairing…'
      }
    }

    ws.onclose = () => {
      statusEl.textContent = 'Disconnected. Reconnecting…'
      state.hello = null
      setTimeout(connect, 800)
    }

    ws.onerror = () => {
      // onclose will handle UX
    }

    ws.onmessage = (ev) => {
      let msg
      try {
        msg = JSON.parse(ev.data)
      } catch {
        return
      }

      if (msg.type === 'hello') {
        state.hello = msg.data
        const s = state.hello.side
        keysEl.textContent = s === 0 ? 'use ' : s === 1 ? 'use ' : ''
        if (s === 0) keysEl.innerHTML = `<kbd>W</kbd>/<kbd>S</kbd>`
        if (s === 1) keysEl.innerHTML = `<kbd>↑</kbd>/<kbd>↓</kbd>`
        if (s === -1) keysEl.textContent = '(spectator/waiting)'
        statusEl.textContent = `Room ${state.hello.roomId} — ${sideName(s)}`

        // Reset smoothed ball for new room/game.
        state.lastServerState = null
        state.lastServerAt = 0
        state.render.ballX = state.game.ballX
        state.render.ballY = state.game.ballY
      }


       if (msg.type === 'state') {
         const prev = state.lastServerState
         const prevGame = state.game
         state.game = msg.data


         // If the ball teleported (score/reset), snap instantly.
         if (prev) {
           const dx = msg.data.ballX - prev.ballX
           const dy = msg.data.ballY - prev.ballY
           if (dx * dx + dy * dy > 140 * 140) {
             state.render.ballX = msg.data.ballX
             state.render.ballY = msg.data.ballY
           }
         } else {
           state.render.ballX = msg.data.ballX
           state.render.ballY = msg.data.ballY
         }

         // Prevent any perceived paddle snap: if the server suddenly changes
         // a paddle by a very large delta, keep the previous value.
         if (prevGame) {
           const maxJump = 160
           for (let i = 0; i < 2; i++) {
             const dy = msg.data.paddleY[i] - prevGame.paddleY[i]
             if (dy * dy > maxJump * maxJump) {
               state.game.paddleY[i] = prevGame.paddleY[i]
             }
           }
         }


        state.lastServerState = msg.data
        state.lastServerAt = performance.now()
      }


      if (msg.type === 'error') {
        statusEl.textContent = `Error: ${msg.data}`
      }
    }
  }

  function canvasToWorldY(clientY) {
    const rect = canvas.getBoundingClientRect()
    const y = ((clientY - rect.top) / rect.height) * canvas.height
    return Math.max(0, Math.min(canvas.height, y))
  }

  // Mouse/touch drag controls: only send while dragging.
  let dragging = false

  function sendDragY(clientY) {
    if (!dragging) return
    send('mouse', { y: canvasToWorldY(clientY) })
  }

  canvas.addEventListener('pointerdown', (e) => {
    dragging = true
    canvas.setPointerCapture(e.pointerId)
    sendDragY(e.clientY)
  })

  canvas.addEventListener('pointermove', (e) => {
    sendDragY(e.clientY)
  })

  canvas.addEventListener('pointerup', (e) => {
    dragging = false
    try {
      canvas.releasePointerCapture(e.pointerId)
    } catch {
      // ignore
    }
  })

  canvas.addEventListener('pointercancel', () => {
    dragging = false
  })

  // On-screen buttons (mobile).
  const btnUp = document.getElementById('btnUp')
  const btnDown = document.getElementById('btnDown')

  function bindHoldButton(btn, dir) {
    if (!btn) return

    let holding = false

    function start(e) {
      e.preventDefault()
      holding = true
      send('move', { dir })
    }

    function end(e) {
      e.preventDefault()
      if (!holding) return
      holding = false
      send('move', { dir: 0 })
    }

    btn.addEventListener('pointerdown', start)
    btn.addEventListener('pointerup', end)
    btn.addEventListener('pointercancel', end)
    btn.addEventListener('pointerleave', end)
  }

  bindHoldButton(btnUp, -1)
  bindHoldButton(btnDown, 1)

  // Keyboard controls.
  const down = new Set()

  function updateKeyboardDir() {
    const side = state.hello?.side
    let dir = 0

    if (side === 0) {
      if (down.has('KeyW')) dir -= 1
      if (down.has('KeyS')) dir += 1
    } else if (side === 1) {
      if (down.has('ArrowUp')) dir -= 1
      if (down.has('ArrowDown')) dir += 1
    } else {
      return
    }

    send('move', { dir })
  }

  window.addEventListener('keydown', (e) => {
    down.add(e.code)
    updateKeyboardDir()
  })

  window.addEventListener('keyup', (e) => {
    down.delete(e.code)
    updateKeyboardDir()
  })

  function lerp(a, b, t) {
    return a + (b - a) * t
  }

  function updateRenderState(now) {
    const snap = state.lastServerState
    if (!snap) return

    // Smooth toward the latest server state over ~60ms (snappier).
    const alpha = Math.max(0, Math.min(1, (now - state.lastServerAt) / 10))

    // Only smooth the ball to reduce visual snaps under load.
    state.render.ballX = lerp(state.render.ballX, snap.ballX, alpha)
    state.render.ballY = lerp(state.render.ballY, snap.ballY, alpha)
  }

  function draw(now) {
    const g = state.game
    updateRenderState(now)

    ctx.clearRect(0, 0, canvas.width, canvas.height)

    // center line
    ctx.strokeStyle = 'rgba(255,255,255,0.15)'
    ctx.setLineDash([10, 10])
    ctx.beginPath()
    ctx.moveTo(canvas.width / 2, 0)
    ctx.lineTo(canvas.width / 2, canvas.height)
    ctx.stroke()
    ctx.setLineDash([])

    // paddles
    const paddleW = 12
    const paddleH = 90
    const margin = 20

    ctx.fillStyle = 'rgba(255,255,255,0.85)'
    ctx.fillRect(margin, g.paddleY[0], paddleW, paddleH)
    ctx.fillRect(canvas.width - margin - paddleW, g.paddleY[1], paddleW, paddleH)

    // ball
    ctx.beginPath()
    ctx.arc(state.render.ballX, state.render.ballY, 8, 0, Math.PI * 2)
    ctx.fill()

    // score + timer
    ctx.fillStyle = 'rgba(255,255,255,0.9)'
    ctx.font = '28px ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace'
    ctx.textAlign = 'center'
    ctx.fillText(`${g.score[0]}   ${g.score[1]}`, canvas.width / 2, 40)

    if (typeof g.secondsLeft === 'number') {
      const m = Math.floor(g.secondsLeft / 60)
      const s = `${g.secondsLeft % 60}`.padStart(2, '0')
      ctx.font = '14px ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace'
      ctx.fillStyle = 'rgba(255,255,255,0.6)'
      ctx.fillText(`${m}:${s}`, canvas.width / 2, 62)
    }

    if (!g.running) {
      ctx.fillStyle = 'rgba(255,255,255,0.7)'
      ctx.font = '18px ui-sans-serif, system-ui'
      ctx.fillText('Waiting for both players…', canvas.width / 2, canvas.height / 2)
    }

    requestAnimationFrame((t) => draw(t))
  }

  connect()
  requestAnimationFrame((t) => draw(t))
})()
