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
    },
  }

  function sideName(side) {
    if (side === 0) return 'Left (W/S)'
    if (side === 1) return 'Right (↑/↓)'
    return 'Waiting for opponent…'
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

  function connect() {
    ws = new WebSocket(wsURL())

    ws.onopen = () => {
      statusEl.textContent = 'Connected. Pairing…'
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
      }

      if (msg.type === 'state') {
        state.game = msg.data
      }
    }
  }

  function canvasToWorldY(clientY) {
    const rect = canvas.getBoundingClientRect()
    const y = ((clientY - rect.top) / rect.height) * canvas.height
    return Math.max(0, Math.min(canvas.height, y))
  }

  // Mouse/touch controls: always send mouse target.
  canvas.addEventListener('mousemove', (e) => {
    send('mouse', { y: canvasToWorldY(e.clientY) })
  })

  canvas.addEventListener(
    'touchstart',
    (e) => {
      const t = e.touches[0]
      if (t) send('mouse', { y: canvasToWorldY(t.clientY) })
    },
    { passive: true },
  )

  canvas.addEventListener(
    'touchmove',
    (e) => {
      const t = e.touches[0]
      if (t) send('mouse', { y: canvasToWorldY(t.clientY) })
    },
    { passive: true },
  )

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

  function draw() {
    const g = state.game

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
    ctx.arc(g.ballX, g.ballY, 8, 0, Math.PI * 2)
    ctx.fill()

    // score + status
    ctx.fillStyle = 'rgba(255,255,255,0.9)'
    ctx.font = '28px ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace'
    ctx.textAlign = 'center'
    ctx.fillText(`${g.score[0]}   ${g.score[1]}`, canvas.width / 2, 40)

    if (!g.running) {
      ctx.fillStyle = 'rgba(255,255,255,0.7)'
      ctx.font = '18px ui-sans-serif, system-ui'
      ctx.fillText('Waiting for both players…', canvas.width / 2, canvas.height / 2)
    }

    requestAnimationFrame(draw)
  }

  connect()
  requestAnimationFrame(draw)
})()
