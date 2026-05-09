/* Agents tool — SSE client + send handler. Attached once per session detail page. */
var AgentsUI = (function () {
  var _base, _sessionID, _es;

  function scrollToBottom() {
    var conv = document.getElementById("conversation");
    if (conv) conv.scrollTop = conv.scrollHeight;
  }

  function appendBubble(role, text) {
    var conv = document.getElementById("conversation");
    if (!conv) return;

    // Remove "no messages" placeholder on first message.
    var placeholder = conv.querySelector("p");
    if (placeholder) placeholder.remove();

    var wrap = document.createElement("div");
    var inner = document.createElement("div");
    inner.style.whiteSpace = "pre-wrap";
    inner.textContent = text;

    if (role === "user") {
      wrap.className = "flex justify-end";
      inner.className =
        "max-w-[75%] rounded-lg bg-green-500 px-4 py-3 text-sm text-white-100";
    } else {
      wrap.className = "flex justify-start";
      inner.className =
        "max-w-[75%] rounded-lg bg-white-200 dark:bg-navy-800 px-4 py-3 text-sm text-black-900 dark:text-white-100 font-mono";
    }
    wrap.appendChild(inner);
    // Insert before stream-output div.
    var streamDiv = document.getElementById("stream-output");
    conv.insertBefore(wrap, streamDiv);
    scrollToBottom();
  }

  function setStatus(msg) {
    var el = document.getElementById("status-msg");
    if (el) el.textContent = msg;
  }

  function setSendEnabled(enabled) {
    var btn = document.getElementById("send-btn");
    if (btn) btn.disabled = !enabled;
  }

  function connectSSE() {
    if (_es) _es.close();
    _es = new EventSource(_base + "/stream?session=" + encodeURIComponent(_sessionID));

    var streamOutput = document.getElementById("stream-output");
    var streamText = document.getElementById("stream-text");
    var accumulated = "";

    _es.onmessage = function (e) {
      var msg;
      try { msg = JSON.parse(e.data); } catch (_) { return; }

      switch (msg.type) {
        case "text_delta":
          accumulated += msg.text;
          if (streamOutput) streamOutput.classList.remove("hidden");
          if (streamText) streamText.textContent = accumulated;
          scrollToBottom();
          break;

        case "done":
          // Flush accumulated text as a permanent bubble.
          if (accumulated) {
            if (streamOutput) streamOutput.classList.add("hidden");
            if (streamText) streamText.textContent = "";
            appendBubble("assistant", accumulated);
            accumulated = "";
          }
          setSendEnabled(true);
          setStatus("");
          break;

        case "error":
          setStatus("Agent error: " + (msg.text || "unknown"));
          setSendEnabled(true);
          accumulated = "";
          if (streamOutput) streamOutput.classList.add("hidden");
          break;
      }
    };

    _es.onerror = function () {
      setStatus("Stream disconnected — reconnecting…");
      setTimeout(connectSSE, 3000);
    };
  }

  function sendMessage() {
    var input = document.getElementById("msg-input");
    if (!input) return;
    var text = input.value.trim();
    if (!text) return;

    input.value = "";
    appendBubble("user", text);
    setSendEnabled(false);
    setStatus("Sending…");

    fetch(_base + "/sessions/" + encodeURIComponent(_sessionID) + "/send", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text: text }),
    })
      .then(function (r) {
        if (!r.ok) return r.text().then(function (t) { throw new Error(t); });
        setStatus("Waiting for agent…");
      })
      .catch(function (err) {
        setStatus("Error: " + err.message);
        setSendEnabled(true);
      });
  }

  function init(opts) {
    _base = opts.base;
    _sessionID = opts.sessionID;

    connectSSE();
    scrollToBottom();

    var btn = document.getElementById("send-btn");
    if (btn) btn.addEventListener("click", sendMessage);

    var input = document.getElementById("msg-input");
    if (input) {
      input.addEventListener("keydown", function (e) {
        if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
          sendMessage();
        }
      });
    }
  }

  return { init: init };
})();
