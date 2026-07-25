(function () {
  var TOKEN_KEY = "mediashare_admin_token";
  var token = localStorage.getItem(TOKEN_KEY) || "";
  var state = null;

  var gate = document.getElementById("gate");
  var consoleEl = document.getElementById("console");
  var gateErr = document.getElementById("gateErr");
  var connBadge = document.getElementById("connBadge");

  // --- Auth gate ---
  function showGate(err) {
    gate.classList.remove("hidden");
    consoleEl.classList.add("hidden");
    gateErr.innerHTML = err ? '<div class="notice err">' + err + "</div>" : "";
  }
  function showConsole() {
    gate.classList.add("hidden");
    consoleEl.classList.remove("hidden");
  }

  document.getElementById("gateBtn").addEventListener("click", tryEnter);
  document.getElementById("token").addEventListener("keydown", function (e) {
    if (e.key === "Enter") tryEnter();
  });
  function tryEnter() {
    var t = document.getElementById("token").value.trim();
    if (!t) { showGate("Please enter the admin token."); return; }
    // Validate by hitting a protected no-op action list (state is public, so
    // probe an admin endpoint that requires auth).
    fetch("/api/admin/ping", { headers: authHeaders(t) }).then(function (r) {
      if (r.ok) {
        token = t;
        localStorage.setItem(TOKEN_KEY, t);
        showConsole();
        connect();
      } else {
        showGate("That token was rejected.");
      }
    }).catch(function () { showGate("Network error."); });
  }

  document.getElementById("logout").addEventListener("click", function (e) {
    e.preventDefault();
    localStorage.removeItem(TOKEN_KEY);
    token = "";
    location.reload();
  });

  function authHeaders(t) {
    return { "Authorization": "Bearer " + (t || token) };
  }

  // --- Admin actions ---
  function action(path, body) {
    return fetch("/api/admin/" + path, {
      method: "POST",
      headers: Object.assign({ "Content-Type": "application/json" }, authHeaders()),
      body: body ? JSON.stringify(body) : "{}",
    }).then(function (r) {
      if (r.status === 401) { showGate("Session expired — re-enter the token."); }
      return r;
    });
  }

  document.getElementById("btnSkip").addEventListener("click", function () { action("skip"); });
  document.getElementById("btnClearQueue").addEventListener("click", function () {
    if (confirm("Clear the approved queue?")) action("clear", { scope: "queue" });
  });
  document.getElementById("btnClearAll").addEventListener("click", function () {
    if (confirm("Clear EVERYTHING — pending, queue, and now playing?")) action("clear", { scope: "all" });
  });
  document.getElementById("btnPause").addEventListener("click", function () {
    action(state && state.paused ? "resume" : "pause");
  });
  document.getElementById("bypass").addEventListener("change", function (e) {
    action("bypass", { enabled: e.target.checked });
  });

  // --- WebSocket ---
  function connect() {
    var proto = location.protocol === "https:" ? "wss:" : "ws:";
    var ws = new WebSocket(proto + "//" + location.host + "/ws?role=admin");
    ws.onopen = function () { connBadge.textContent = "live"; };
    ws.onmessage = function (ev) {
      var msg;
      try { msg = JSON.parse(ev.data); } catch (e) { return; }
      if (msg.type === "state") render(msg.payload);
    };
    ws.onclose = function () {
      connBadge.textContent = "reconnecting…";
      setTimeout(connect, 1500);
    };
    ws.onerror = function () { try { ws.close(); } catch (e) {} };
  }

  // --- Render ---
  function render(s) {
    state = s;

    // now playing
    var np = document.getElementById("nowPlaying");
    if (s.nowPlaying) {
      np.innerHTML = itemCard(s.nowPlaying, "playing", []);
    } else {
      np.innerHTML = '<div class="empty">Nothing is playing right now.</div>';
    }

    // pause button label
    document.getElementById("btnPause").innerHTML = s.paused ? "▶ Resume" : "⏸ Pause";
    document.getElementById("bypass").checked = !!s.bypass;

    // pending
    var pending = document.getElementById("pending");
    document.getElementById("pendingCount").textContent = s.pending.length;
    if (s.pending.length === 0) {
      pending.innerHTML = '<div class="empty">Nothing waiting for review.</div>';
    } else {
      pending.innerHTML = s.pending.map(function (it) {
        return itemCard(it, "pending", [
          { label: "✓ Approve", cls: "green", act: "approve" },
          { label: "✕ Reject", cls: "red", act: "reject" },
        ]);
      }).join("");
    }

    // queue
    var queue = document.getElementById("queue");
    document.getElementById("queueCount").textContent = s.queue.length;
    if (s.queue.length === 0) {
      queue.innerHTML = '<div class="empty">Queue is empty.</div>';
    } else {
      queue.innerHTML = s.queue.map(function (it, i) {
        return itemCard(it, null, [
          { label: "Remove", cls: "red ghost", act: "remove" },
        ], i + 1);
      }).join("");
    }

    wireItemButtons();
  }

  function itemCard(it, pill, actions, index) {
    var thumb = "";
    if (it.type === "youtube") {
      thumb = '<img src="https://img.youtube.com/vi/' + escapeAttr(it.youtubeId) + '/mqdefault.jpg" alt="">';
    } else {
      thumb = '<video src="' + escapeAttr(it.mediaUrl) + '" muted preload="metadata"></video>';
    }
    var pillHtml = "";
    if (pill === "playing") pillHtml = '<span class="pill playing">playing</span> ';
    else if (pill === "pending") pillHtml = '<span class="pill pending">pending</span> ';
    var typePill = '<span class="pill ' + it.type + '">' + it.type + "</span>";

    var sub = [];
    if (index) sub.push("#" + index);
    if (it.type === "youtube" && it.startSeconds) sub.push("start " + fmtTime(it.startSeconds));
    sub.push(it.durationSeconds > 0 ? "plays " + it.durationSeconds + "s" : "full length");
    if (it.submitterName) sub.push("by " + escapeHtml(it.submitterName));

    var actionsHtml = actions.map(function (a) {
      return '<button class="btn ' + a.cls + '" data-act="' + a.act + '" data-id="' + escapeAttr(it.id) + '">' + a.label + "</button>";
    }).join("");

    return (
      '<div class="item">' +
        '<div class="thumb">' + thumb + "</div>" +
        '<div class="meta">' +
          '<div class="title">' + pillHtml + escapeHtml(it.title) + "</div>" +
          '<div class="sub">' + typePill + " · " + sub.join(" · ") + "</div>" +
        "</div>" +
        '<div class="actions">' + actionsHtml + "</div>" +
      "</div>"
    );
  }

  function wireItemButtons() {
    Array.prototype.forEach.call(document.querySelectorAll("[data-act]"), function (b) {
      b.addEventListener("click", function () {
        var act = b.getAttribute("data-act");
        var id = b.getAttribute("data-id");
        action(act, { id: id });
      });
    });
  }

  // --- utils ---
  function fmtTime(secs) {
    var m = Math.floor(secs / 60), s = secs % 60;
    return m + ":" + (s < 10 ? "0" : "") + s;
  }
  function escapeHtml(s) {
    return String(s || "").replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }
  function escapeAttr(s) { return escapeHtml(s); }

  // --- boot ---
  if (token) {
    fetch("/api/admin/ping", { headers: authHeaders() }).then(function (r) {
      if (r.ok) { showConsole(); connect(); } else { showGate(); }
    }).catch(function () { showGate(); });
  } else {
    showGate();
  }
})();
