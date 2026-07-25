(function () {
  var state = null;
  var connBadge = document.getElementById("connBadge");

  // Auth is cookie-based and enforced server-side (this page is only served to
  // logged-in streamers). On a 401 from any API call, bounce to /login.
  function onUnauthorized() { window.location = "/login"; }

  // --- Admin actions ---
  function action(path, body) {
    return fetch("/api/admin/" + path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: body ? JSON.stringify(body) : "{}",
    }).then(function (r) {
      if (r.status === 401) onUnauthorized();
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

  document.getElementById("logout").addEventListener("click", function (e) {
    e.preventDefault();
    document.getElementById("logoutForm").submit();
  });
  document.getElementById("openPlayer").addEventListener("click", function (e) {
    e.preventDefault();
    window.open(document.getElementById("playerLink").value, "_blank");
  });
  document.getElementById("playerCopyBtn").addEventListener("click", function () {
    copyFrom("playerLink", "playerCopyBtn");
  });

  // --- WebSocket (room resolved from the login cookie server-side) ---
  function connect() {
    var proto = location.protocol === "https:" ? "wss:" : "ws:";
    var ws = new WebSocket(proto + "//" + location.host + "/ws?role=admin");
    ws.onopen = function () { connBadge.textContent = "live"; };
    ws.onmessage = function (ev) {
      var msg;
      try { msg = JSON.parse(ev.data); } catch (e) { return; }
      if (msg.type === "state") render(msg.payload);
      else if (msg.type === "session") loadSession();
    };
    ws.onclose = function () {
      connBadge.textContent = "reconnecting…";
      setTimeout(connect, 1500);
    };
    ws.onerror = function () { try { ws.close(); } catch (e) {} };
  }

  // --- Render queue state ---
  function render(s) {
    state = s;

    var np = document.getElementById("nowPlaying");
    if (s.nowPlaying) {
      np.innerHTML = itemCard(s.nowPlaying, "playing", []);
    } else {
      np.innerHTML = '<div class="empty">Nothing is playing right now.</div>';
    }

    document.getElementById("btnPause").innerHTML = s.paused ? "▶ Resume" : "⏸ Pause";
    document.getElementById("bypass").checked = !!s.bypass;

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

    var queue = document.getElementById("queue");
    document.getElementById("queueCount").textContent = s.queue.length;
    if (s.queue.length === 0) {
      queue.innerHTML = '<div class="empty">Queue is empty.</div>';
    } else {
      queue.innerHTML = s.queue.map(function (it, i) {
        return itemCard(it, null, [{ label: "Remove", cls: "red ghost", act: "remove" }], i + 1);
      }).join("");
    }

    wireItemButtons();
  }

  function itemCard(it, pill, actions, index) {
    var thumb = it.type === "youtube"
      ? '<img src="https://img.youtube.com/vi/' + escapeAttr(it.youtubeId) + '/mqdefault.jpg" alt="">'
      : '<video src="' + escapeAttr(it.mediaUrl) + '" muted preload="metadata"></video>';

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
        action(b.getAttribute("data-act"), { id: b.getAttribute("data-id") });
      });
    });
  }

  // --- Media share session ---
  var sessionUptimeTimer = null;
  var sessionStartedAt = null;

  function loadSession() {
    fetch("/api/admin/session")
      .then(function (r) { if (r.status === 401) { onUnauthorized(); return null; } return r.ok ? r.json() : null; })
      .then(function (v) { if (v) renderSession(v); })
      .catch(function () {});
  }

  function renderSession(v) {
    var closed = document.getElementById("sessionClosed");
    var open = document.getElementById("sessionOpen");
    var status = document.getElementById("sessionStatus");

    if (v.active) {
      closed.classList.add("hidden");
      open.classList.remove("hidden");
      status.innerHTML = '<div class="notice ok">🟢 Media share is OPEN — viewers can submit.</div>';
      document.getElementById("sessionLink").value = v.link || "";
      sessionStartedAt = v.startedAt ? new Date(v.startedAt) : null;
      startUptime();
    } else {
      open.classList.add("hidden");
      closed.classList.remove("hidden");
      status.innerHTML = '<div class="notice" style="background:rgba(154,160,174,.12);border:1px solid var(--border);color:var(--muted)">⚫ Media share is closed.</div>';
      sessionStartedAt = null;
      stopUptime();
    }
  }

  function startUptime() { stopUptime(); tickUptime(); sessionUptimeTimer = setInterval(tickUptime, 1000); }
  function stopUptime() {
    if (sessionUptimeTimer) { clearInterval(sessionUptimeTimer); sessionUptimeTimer = null; }
    document.getElementById("sessionUptime").textContent = "";
  }
  function tickUptime() {
    if (!sessionStartedAt) return;
    var secs = Math.max(0, Math.floor((Date.now() - sessionStartedAt.getTime()) / 1000));
    var h = Math.floor(secs / 3600), m = Math.floor((secs % 3600) / 60), s = secs % 60;
    var parts = (h > 0 ? h + "h " : "") + (m < 10 ? "0" : "") + m + "m " + (s < 10 ? "0" : "") + s + "s";
    document.getElementById("sessionUptime").textContent = "open for " + parts;
  }

  function sessionAction(path) {
    return action("session/" + path)
      .then(function (r) { return r && r.ok ? r.json() : null; })
      .then(function (v) { if (v) renderSession(v); });
  }

  document.getElementById("sessionStartBtn").addEventListener("click", function () { sessionAction("start"); });
  document.getElementById("sessionStopBtn").addEventListener("click", function () {
    if (confirm("Stop media share? The current invite link will stop working.")) sessionAction("stop");
  });
  document.getElementById("sessionRegenBtn").addEventListener("click", function () {
    if (confirm("Generate a new link? The old link will stop working immediately.")) sessionAction("regenerate");
  });
  document.getElementById("sessionCopyBtn").addEventListener("click", function () {
    copyFrom("sessionLink", "sessionCopyBtn");
  });

  // --- utils ---
  function copyFrom(inputId, btnId) {
    var input = document.getElementById(inputId);
    input.select();
    var done = function () {
      var b = document.getElementById(btnId);
      var old = b.textContent; b.textContent = "✓ Copied";
      setTimeout(function () { b.textContent = old; }, 1200);
    };
    if (navigator.clipboard) {
      navigator.clipboard.writeText(input.value).then(done, function () { document.execCommand("copy"); done(); });
    } else {
      document.execCommand("copy"); done();
    }
  }
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
  connect();
  loadSession();
})();
