(function () {
  var stage = document.getElementById("stage");
  var idle = document.getElementById("idle");
  var overlay = document.getElementById("overlay");
  var ovTitle = document.getElementById("ovTitle");
  var ovBy = document.getElementById("ovBy");

  var ytReady = false;   // YouTube IFrame API loaded
  var ytPlayer = null;   // reusable YT player instance
  var current = null;    // the item currently being rendered
  var paused = false;

  // pendingItem: item we want to play but couldn't yet (YT API not ready).
  var pendingItem = null;

  // Duration-cap timer with pause/resume support.
  var timer = { handle: null, endsAt: 0, remaining: 0, itemId: null };

  // --- YouTube API bootstrap ---
  window.onYouTubeIframeAPIReady = function () {
    ytReady = true;
    if (pendingItem) {
      var it = pendingItem;
      pendingItem = null;
      startItem(it);
    }
  };

  // --- WebSocket ---
  function connect() {
    var proto = location.protocol === "https:" ? "wss:" : "ws:";
    var ws = new WebSocket(proto + "//" + location.host + "/ws?role=player");
    ws.onmessage = function (ev) {
      var msg;
      try { msg = JSON.parse(ev.data); } catch (e) { return; }
      if (msg.type === "state") applyState(msg.payload);
    };
    ws.onclose = function () { setTimeout(connect, 1500); };
    ws.onerror = function () { try { ws.close(); } catch (e) {} };
  }

  function applyState(s) {
    if (!s) return;
    paused = !!s.paused;
    var np = s.nowPlaying;

    if (!np) {
      current = null;
      clearTimer();
      teardown();
      showIdle(true);
      return;
    }

    if (!current || current.id !== np.id) {
      startItem(np);
    } else {
      // Same item — just reconcile pause state.
      applyPause();
    }
  }

  // --- Playback ---
  function startItem(it) {
    current = it;
    clearTimer();
    showIdle(false);
    setOverlay(it);

    if (it.type === "youtube") {
      if (!ytReady) { pendingItem = it; return; }
      playYouTube(it);
    } else {
      playUpload(it);
    }
  }

  function playYouTube(it) {
    // Remove any <video> from a previous upload.
    removeUploadEl();
    if (!ytPlayer) {
      var host = document.createElement("div");
      host.id = "ytplayer";
      stage.appendChild(host);
      ytPlayer = new YT.Player("ytplayer", {
        width: "100%",
        height: "100%",
        videoId: it.youtubeId,
        playerVars: { autoplay: 1, controls: 0, rel: 0, modestbranding: 1, start: it.startSeconds || 0, playsinline: 1 },
        events: {
          onReady: function (e) { e.target.playVideo(); startDurationTimer(it); },
          onStateChange: function (e) {
            if (e.data === YT.PlayerState.ENDED) reportEnded(it.id);
            if (e.data === YT.PlayerState.PLAYING && paused) e.target.pauseVideo();
          },
        },
      });
    } else {
      ytPlayer.loadVideoById({ videoId: it.youtubeId, startSeconds: it.startSeconds || 0 });
      startDurationTimer(it);
    }
  }

  function playUpload(it) {
    if (ytPlayer) { try { ytPlayer.stopVideo(); } catch (e) {} }
    removeUploadEl();
    var v = document.createElement("video");
    v.id = "uploadVideo";
    v.src = it.mediaUrl;
    v.autoplay = true;
    v.controls = false;
    v.playsInline = true;
    v.addEventListener("ended", function () { reportEnded(it.id); });
    v.addEventListener("playing", function () { if (paused) v.pause(); });
    stage.appendChild(v);
    v.play().catch(function () {/* autoplay may require a muted fallback */});
    startDurationTimer(it);
  }

  function removeUploadEl() {
    var v = document.getElementById("uploadVideo");
    if (v) v.remove();
  }

  function teardown() {
    removeUploadEl();
    if (ytPlayer) { try { ytPlayer.stopVideo(); } catch (e) {} }
    overlay.classList.add("hidden");
  }

  // --- Duration timer (pause-aware) ---
  function startDurationTimer(it) {
    clearTimer();
    var secs = it.durationSeconds || 0;
    if (secs <= 0) return; // play to natural end
    timer.itemId = it.id;
    timer.remaining = secs * 1000;
    if (!paused) armTimer();
  }

  function armTimer() {
    timer.endsAt = Date.now() + timer.remaining;
    timer.handle = setTimeout(function () {
      timer.handle = null;
      reportEnded(timer.itemId);
    }, timer.remaining);
  }

  function pauseTimer() {
    if (timer.handle) {
      clearTimeout(timer.handle);
      timer.handle = null;
      timer.remaining = Math.max(0, timer.endsAt - Date.now());
    }
  }

  function clearTimer() {
    if (timer.handle) clearTimeout(timer.handle);
    timer.handle = null;
    timer.remaining = 0;
    timer.itemId = null;
  }

  // --- Pause/resume reconciliation ---
  function applyPause() {
    overlay.classList.toggle("paused", paused);
    if (paused) {
      pauseTimer();
      if (current && current.type === "youtube" && ytPlayer) { try { ytPlayer.pauseVideo(); } catch (e) {} }
      var v = document.getElementById("uploadVideo");
      if (v) v.pause();
    } else {
      if (timer.itemId && timer.remaining > 0 && !timer.handle) armTimer();
      if (current && current.type === "youtube" && ytPlayer) { try { ytPlayer.playVideo(); } catch (e) {} }
      var v2 = document.getElementById("uploadVideo");
      if (v2) v2.play().catch(function () {});
    }
  }

  // --- Advance ---
  var lastReported = null;
  function reportEnded(id) {
    if (!id || lastReported === id) return;
    lastReported = id;
    clearTimer();
    fetch("/api/player/ended", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: id }),
    }).catch(function () {});
    // The server will broadcast a new state; UI updates from applyState.
  }

  // --- UI helpers ---
  function showIdle(show) { idle.classList.toggle("hidden", !show); }
  function setOverlay(it) {
    ovTitle.textContent = it.title || "";
    ovBy.textContent = it.submitterName ? "· by " + it.submitterName : "";
    overlay.classList.remove("hidden");
    overlay.classList.toggle("paused", paused);
  }

  connect();
})();
