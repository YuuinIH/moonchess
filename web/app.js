const pieces = {
  p: "♟",
  r: "♜",
  n: "♞",
  b: "♝",
  q: "♛",
  k: "♚",
  P: "♙",
  R: "♖",
  N: "♘",
  B: "♗",
  Q: "♕",
  K: "♔",
};
let me,
  current,
  control,
  selected = null,
  stream,
  replay = false,
  pendingFinish = null,
  serverOffset = 0;
const $ = (s) => document.querySelector(s);
async function api(path, method = "GET", body) {
  const r = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await r.json();
  if (!r.ok) throw Error(data.error || r.statusText);
  return data;
}
function position(fen) {
  const out = [];
  for (const row of fen.split(" ")[0].split("/"))
    for (const c of row) {
      if (/[1-8]/.test(c)) for (let i = 0; i < Number(c); i++) out.push("");
      else out.push(c);
    }
  return out;
}
function squareName(i) {
  return "abcdefgh"[i % 8] + (8 - Math.floor(i / 8));
}
function color() {
  return control?.white_client_id === me?.client_id ? "white" : "black";
}
function draw(state) {
  const board = $("#board");
  board.innerHTML = "";
  let squares = position(state.fen).map((p, i) => ({ p, i }));
  if (color() === "black") squares.reverse();
  for (const { p, i } of squares) {
    const b = document.createElement("button");
    b.className = "square " + ((Math.floor(i / 8) + i) % 2 ? "dark" : "");
    if (selected === i) b.classList.add("selected");
    b.textContent = pieces[p] || "";
    b.setAttribute("aria-label", squareName(i) + (p ? " " + p : ""));
    b.onclick = () => clickSquare(i, p);
    board.appendChild(b);
  }
}
async function clickSquare(i, piece) {
  if (
    replay ||
    !current ||
    current.status !== "active" ||
    current.turn !== color()
  )
    return;
  if (selected === null) {
    if (!piece || (color() === "white") !== (piece === piece.toUpperCase()))
      return;
    selected = i;
    draw(current);
    return;
  }
  let move = squareName(selected) + squareName(i);
  const from = position(current.fen)[selected];
  if (from?.toLowerCase() === "p" && (i < 8 || i >= 56)) {
    let promotion = prompt("Promote to q, r, b or n", "q");
    if (!/^[qrbn]$/.test(promotion || "")) {
      selected = null;
      draw(current);
      return;
    }
    move += promotion;
  }
  selected = null;
  draw(current);
  try {
    const body = await api(`/api/games/${control.gameId}/moves`, "POST", {
      move,
    });
    updateGame(body);
    if (current.status === "active") $("#message").textContent = `Committed ${move}`;
  } catch (e) {
    $("#message").textContent = e.message;
  }
}
function setView(name) {
  $("#view").textContent = name;
  $("#find").hidden = name !== "Home";
  $("#cancel").hidden = name !== "Searching";
  $("#again").hidden = name !== "Finished";
  $("#replay").hidden = name !== "Finished";
  $("#board").hidden = name === "Home" || name === "Searching";
  $("#reset").disabled = name === "Game";
  $("#finish-actions").hidden = name !== "Game";
  $("#clocks").hidden = !current?.clock || (name !== "Game" && name !== "Finished");
  if (name !== "Game") {
    pendingFinish = null;
    $("#finish-confirm").hidden = true;
  }
  $("#migrate").disabled = name === "Home" || name === "Searching";
  $("#message").textContent =
    name === "Searching"
      ? "Waiting for another browser…"
      : name === "Home"
        ? "Open another browser or private window and Find Match."
        : name === "Finished"
          ? `Finished: ${current.status}${current.reason ? " · " + current.reason : ""}`
          : `You are ${color()}. ${current.turn === color() ? "Your turn." : "Opponent’s turn."}`;
}
function updateGame(body) {
  if (control?.gameId === body.control.gameId &&
      (body.control.committedSeq < control.committedSeq ||
       (body.control.committedSeq === control.committedSeq && body.control.epoch < control.epoch))) return;
  if (body.serverTimeUnixMs) serverOffset = body.serverTimeUnixMs - Date.now();
  if (control?.gameId !== body.control.gameId) {
    pendingFinish = null;
    $("#finish-confirm").hidden = true;
  }
  current = body.game;
  control = body.control;
  selected = null;
  replay = false;
  $("#replay-position").hidden = true;
  draw(current);
  renderControl(control);
  setView(current.status === "active" ? "Game" : "Finished");
  renderClocks();
}
function renderControl(c) {
  $("#game-id").textContent = c.gameId;
  $("#owner").textContent = c.ownerId || "between owners";
  $("#epoch").textContent = c.epoch;
  $("#sequence").textContent = c.committedSeq;
  $("#lease").textContent = c.phase;
  $("#checkpoint").textContent = `${c.checkpointSeq} · ${c.checkpointRef}`;
  $("#moves").textContent = (current.moves || []).join(" ");
}
function renderClocks() {
  const c = current?.clock;
  if (!c) return;
  const elapsed = current.status === "active" && c.turnStartedUnixMs > 0
    ? Math.max(0, Date.now() + serverOffset - c.turnStartedUnixMs) : 0;
  for (const side of ["white", "black"]) {
    const ms = Math.max(0, c[side + "Ms"] - (current.turn === side ? elapsed : 0));
    const seconds = Math.ceil(ms / 1000);
    const clock = $("#" + side + "-clock");
    clock.textContent = `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
    clock.classList.toggle("running", current.status === "active" && current.turn === side);
    clock.classList.toggle("low", ms < 30000);
  }
  $("#time-control").textContent = `+${c.incrementMs / 1000}s per move${current.status !== "active" ? " · stopped" : ""}`;
}
setInterval(renderClocks, 100);
function confirmFinish(kind) {
  pendingFinish = kind;
  $("#finish-warning").textContent = kind === "resign"
    ? "Resign this game? Your opponent wins."
    : "Abandon this game? This ends the game and counts as a loss. Closing the page alone allows you to reconnect.";
  $("#confirm-finish").textContent = kind === "resign" ? "Confirm resignation" : "Confirm abandonment";
  $("#finish-confirm").hidden = false;
}
$("#resign").onclick = () => confirmFinish("resign");
$("#abandon").onclick = () => confirmFinish("abandon");
$("#cancel-finish").onclick = () => {
  pendingFinish = null;
  $("#finish-confirm").hidden = true;
};
$("#confirm-finish").onclick = () => action(async () => {
  if (!pendingFinish || !control || current?.status !== "active") return;
  const button = $("#confirm-finish");
  button.disabled = true;
  try {
    updateGame(await api(`/api/games/${control.gameId}/${pendingFinish}`, "POST"));
  } finally { button.disabled = false; }
});
function log(kind, data) {
  const li = document.createElement("li");
  li.textContent = `${new Date().toLocaleTimeString()} ${kind} ${JSON.stringify(data)}`;
  $("#events").prepend(li);
  while ($("#events").children.length > 30) $("#events").lastChild.remove();
}
function connect() {
  stream?.close();
  stream = new EventSource("/api/events");
  stream.onopen = () => {
    $("#connection").textContent = "SSE connected";
  };
  stream.onerror = () => {
    $("#connection").textContent = "Reconnecting…";
  };
  stream.addEventListener("matchmaking", (e) => {
    const b = JSON.parse(e.data);
    me = b.client;
    $("#nickname").value = me.nickname;
    $("#client-id").textContent = me.client_id;
    if (b.status === "home") setView("Home");
    if (b.status === "searching") setView("Searching");
  });
  stream.addEventListener("game_state", (e) => {
    const b = JSON.parse(e.data);
    updateGame(b);
    $("#materialization").textContent = JSON.stringify(b.debug, null, 2);
  });
  for (const kind of [
    "move_committed",
    "ownership",
    "ownership_state",
    "game_finished",
  ])
    stream.addEventListener(kind, (e) => log(kind, JSON.parse(e.data)));
}
async function action(fn) {
  try {
    await fn();
  } catch (e) {
    $("#message").textContent = e.message;
  }
}
$("#find").onclick = () =>
  action(() => api("/api/matchmaking/enqueue", "POST"));
$("#cancel").onclick = () =>
  action(() => api("/api/matchmaking/enqueue", "DELETE"));
$("#again").onclick = () =>
  action(() => api("/api/games/current/play-again", "POST"));
$("#rename").onclick = () =>
  action(async () => {
    me = await api("/api/me", "PATCH", { nickname: $("#nickname").value });
  });
$("#reset").onclick = () =>
  action(async () => {
    me = await api("/api/me/reset", "POST");
    current = null;
    control = null;
    connect();
    setView("Home");
  });
$("#migrate").onclick = () =>
  action(() => api(`/api/games/${control.gameId}/migrate`, "POST", {}));
// Replay is local and derives every position from the server's legal UCI history.
function replayFEN(moves) {
  let board = position("rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR");
  let ep = -1;
  for (const m of moves) {
    const idx = (s) =>
      ("8".charCodeAt(0) - s.charCodeAt(1)) * 8 + "abcdefgh".indexOf(s[0]);
    const a = idx(m.slice(0, 2)),
      b = idx(m.slice(2, 4));
    let p = board[a];
    if (p.toLowerCase() === "p" && b === ep && !board[b])
      board[b + (p === "P" ? 8 : -8)] = "";
    ep = p.toLowerCase() === "p" && Math.abs(a - b) === 16 ? (a + b) / 2 : -1;
    if (p.toLowerCase() === "k" && Math.abs(a - b) === 2) {
      let rook = a + (b > a ? 3 : -4),
        dest = a + (b > a ? 1 : -1);
      board[dest] = board[rook];
      board[rook] = "";
    }
    board[b] = m[4] ? (p === "P" ? m[4].toUpperCase() : m[4]) : p;
    board[a] = "";
  }
  const rows = [];
  for (let r = 0; r < 8; r++) {
    let row = "",
      empty = 0;
    for (let c = 0; c < 8; c++) {
      let p = board[r * 8 + c];
      if (!p) {
        empty++;
        continue;
      }
      if (empty) {
        row += empty;
        empty = 0;
      }
      row += p;
    }
    if (empty) row += empty;
    rows.push(row);
  }
  return rows.join("/") + " w - - 0 1";
}
$("#replay").onclick = () => {
  replay = !replay;
  $("#replay-position").hidden = !replay;
  $("#replay-position").max = current.moves.length;
  $("#replay-position").value = 0;
  draw(replay ? { fen: replayFEN([]) } : current);
};
$("#replay-position").oninput = (e) => {
  draw({ fen: replayFEN(current.moves.slice(0, Number(e.target.value))) });
  $("#message").textContent =
    `Replay ${e.target.value}/${current.moves.length}`;
};
(async () => {
  await action(async () => {
    me = await api("/api/me");
    $("#nickname").value = me.nickname;
    $("#client-id").textContent = me.client_id;
    setView("Home");
    if (me.game_id) updateGame(await api("/api/games/current"));
    connect();
  });
})();
