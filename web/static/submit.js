(function () {
  var currentType = "youtube";
  var toggle = document.getElementById("typeToggle");
  var fieldType = document.getElementById("fieldType");
  var ytFields = document.getElementById("youtubeFields");
  var upFields = document.getElementById("uploadFields");
  var form = document.getElementById("submitForm");
  var result = document.getElementById("result");
  var btn = document.getElementById("submitBtn");

  // --- Session gating ---
  var token = window.__SESSION_TOKEN__ || "";
  var closedCard = document.getElementById("closedCard");
  var formCard = document.getElementById("formCard");
  var closedMsg = document.getElementById("closedMsg");

  function showClosed(msg) {
    formCard.classList.add("hidden");
    closedCard.classList.remove("hidden");
    if (msg) closedMsg.textContent = msg;
  }
  function showForm() {
    closedCard.classList.add("hidden");
    formCard.classList.remove("hidden");
  }
  function checkSession() {
    if (!token) { showClosed(); return Promise.resolve(false); }
    return fetch("/api/session/check?s=" + encodeURIComponent(token))
      .then(function (r) { return r.json(); })
      .then(function (j) {
        if (j && j.valid) { showForm(); return true; }
        showClosed();
        return false;
      })
      .catch(function () { return false; });
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
  });

  function notice(kind, msg) {
    result.innerHTML = '<div class="notice ' + kind + '">' + msg + "</div>";
  }

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    result.innerHTML = "";

    var fd = new FormData();
    fd.append("type", currentType);
    fd.append("session", token);
    fd.append("name", document.getElementById("name").value.trim());

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
          if (res.status === 403) { showClosed(res.body.error); return; }
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
  checkSession();
  setInterval(checkSession, 10000);
})();
