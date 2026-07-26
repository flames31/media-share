(function () {
  var currentType = "youtube";
  var toggle = document.getElementById("typeToggle");
  var fieldType = document.getElementById("fieldType");
  var ytFields = document.getElementById("youtubeFields");
  var upFields = document.getElementById("uploadFields");
  var form = document.getElementById("submitForm");
  var result = document.getElementById("result");
  var btn = document.getElementById("submitBtn");

  var token = window.__SESSION_TOKEN__ || "";
  var loggedIn = !!window.__LOGGED_IN__;
  var creditsEnabled = !!window.__CREDITS_ENABLED__;
  var creditsPerSecond = Number(window.__CREDITS_PER_SECOND__) || 0;
  var minBillable = 10; // mirrors the server's minBillableSeconds

  var closedCard = document.getElementById("closedCard");
  var loginCard = document.getElementById("loginCard");
  var formCard = document.getElementById("formCard");
  var closedMsg = document.getElementById("closedMsg");
  var costValue = document.getElementById("costValue");
  var balanceBadge = document.getElementById("balanceBadge");

  // Point the Twitch login button back to this submit link after auth.
  var loginBtn = document.getElementById("loginBtn");
  if (loginBtn && token) loginBtn.href = "/viewer/auth/start?s=" + encodeURIComponent(token);

  function hide(el) { if (el) el.classList.add("hidden"); }
  function show(el) { if (el) el.classList.remove("hidden"); }

  function showClosed(msg) {
    hide(loginCard); hide(formCard); show(closedCard);
    if (msg) closedMsg.textContent = msg;
  }
  function showLogin() { hide(closedCard); hide(formCard); show(loginCard); }
  function showForm() {
    hide(closedCard); hide(loginCard); show(formCard);
    loadBalance();
  }

  // Session must be open; then either the login prompt or the form.
  function checkSession() {
    if (!token) { showClosed(); return Promise.resolve(false); }
    return fetch("/api/session/check?s=" + encodeURIComponent(token))
      .then(function (r) { return r.json(); })
      .then(function (j) {
        if (!j || !j.valid) { showClosed(); return false; }
        if (!loggedIn) { showLogin(); return false; }
        showForm();
        return true;
      })
      .catch(function () { return false; });
  }

  // --- Credits ---
  function billable(secs) {
    if (creditsEnabled && secs < minBillable) return minBillable;
    return secs;
  }
  function currentDuration() {
    var raw = currentType === "youtube"
      ? document.getElementById("durationYt").value
      : document.getElementById("durationUp").value;
    var n = parseInt(raw, 10);
    return isNaN(n) || n < 0 ? 0 : n;
  }
  function updateCost() {
    if (!creditsEnabled || !costValue) return;
    costValue.textContent = String(billable(currentDuration()) * creditsPerSecond);
  }
  function loadBalance() {
    if (!creditsEnabled || !balanceBadge || !loggedIn) return;
    fetch("/api/viewer/me?s=" + encodeURIComponent(token))
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (v) { if (v) balanceBadge.textContent = v.balance + " credits"; })
      .catch(function () {});
  }

  toggle.addEventListener("click", function (e) {
    var b = e.target.closest("button[data-type]");
    if (!b) return;
    currentType = b.getAttribute("data-type");
    fieldType.value = currentType;
    Array.prototype.forEach.call(toggle.children, function (c) {
      c.classList.toggle("active", c === b);
    });
    ytFields.classList.toggle("hidden", currentType !== "youtube");
    upFields.classList.toggle("hidden", currentType !== "upload");
    result.innerHTML = "";
    updateCost();
  });

  var durYt = document.getElementById("durationYt");
  var durUp = document.getElementById("durationUp");
  if (durYt) durYt.addEventListener("input", updateCost);
  if (durUp) durUp.addEventListener("input", updateCost);

  // Dev-only: one-click credit top-up (the button only exists when DEV_LOGIN=1).
  // The server resolves the channel from the session token, so nothing to fill in.
  var devCreditBtn = document.getElementById("devCreditBtn");
  if (devCreditBtn) {
    devCreditBtn.addEventListener("click", function () {
      var orig = devCreditBtn.textContent;
      devCreditBtn.disabled = true;
      fetch("/api/dev/credit", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ s: token }),
      })
        .then(function (r) { return r.json().then(function (j) { return { ok: r.ok, body: j }; }); })
        .then(function (res) {
          if (!res.ok) { notice("err", res.body.error || "Could not add credits."); return; }
          if (balanceBadge) balanceBadge.textContent = res.body.balance + " credits";
          devCreditBtn.textContent = "✓ +" + res.body.granted;
          setTimeout(function () { devCreditBtn.textContent = orig; }, 1200);
        })
        .catch(function () { notice("err", "Network error adding credits."); })
        .finally(function () { devCreditBtn.disabled = false; });
    });
  }

  function notice(kind, msg) {
    result.innerHTML = '<div class="notice ' + kind + '">' + msg + "</div>";
  }

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    result.innerHTML = "";

    var fd = new FormData();
    fd.append("type", currentType);
    fd.append("session", token);

    if (currentType === "youtube") {
      var url = document.getElementById("url").value.trim();
      if (!url) { notice("err", "Please paste a YouTube link."); return; }
      fd.append("url", url);
      fd.append("start", document.getElementById("start").value.trim());
      fd.append("duration", document.getElementById("durationYt").value || "10");
    } else {
      var file = document.getElementById("file").files[0];
      if (!file) { notice("err", "Please choose a file to upload."); return; }
      fd.append("file", file);
      fd.append("duration", document.getElementById("durationUp").value || "0");
    }

    btn.disabled = true;
    btn.textContent = "Submitting…";

    fetch("/api/submit", { method: "POST", body: fd })
      .then(function (r) { return r.json().then(function (j) { return { ok: r.ok, status: r.status, body: j }; }); })
      .then(function (res) {
        if (!res.ok) {
          if (res.status === 401) { loggedIn = false; showLogin(); return; }
          if (res.status === 403) { showClosed(res.body.error); return; }
          if (res.status === 402) {
            notice("err", (res.body.error || "Not enough credits.") +
              " (needs " + res.body.cost + ", you have " + res.body.balance + ")");
            loadBalance();
            return;
          }
          notice("err", res.body.error || "Submission failed.");
          return;
        }
        var b = res.body;
        var where = b.status === "approved"
          ? "It was auto-approved and is now in the play queue."
          : "It's now waiting for a moderator to review it.";
        notice("ok", "✅ Added: <strong>" + escapeHtml(b.title) + "</strong>. " + where);
        form.reset();
        document.getElementById("durationYt").value = "10";
        document.getElementById("durationUp").value = "0";
        updateCost();
        loadBalance();
      })
      .catch(function () { notice("err", "Network error. Please try again."); })
      .finally(function () {
        btn.disabled = false;
        btn.textContent = "Add to queue";
      });
  });

  function escapeHtml(s) {
    return String(s || "").replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  // Validate on load, then poll so the page closes promptly if the streamer
  // stops the session (or regenerates the link) while it's open.
  updateCost();
  checkSession();
  setInterval(checkSession, 10000);
})();
