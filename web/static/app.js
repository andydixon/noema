/* Noema UI enhancements. No frameworks, no inline scripts (CSP). */
(function () {
  "use strict";
  const root = document.documentElement;
  const csrf = () => (document.querySelector('meta[name="csrf-token"]') || {}).content || "";

  // Theme: auto → light → dark, remembered per browser.
  try {
    const saved = localStorage.getItem("noema.theme");
    if (saved) root.setAttribute("data-theme", saved);
  } catch (e) { /* storage unavailable */ }
  function cycleTheme() {
    const order = ["auto", "light", "dark"];
    const cur = root.getAttribute("data-theme") || "auto";
    const next = order[(order.indexOf(cur) + 1) % order.length];
    root.setAttribute("data-theme", next);
    try { localStorage.setItem("noema.theme", next); } catch (e) { /* ignore */ }
  }

  // fetch wrapper that carries the CSRF token and same-origin credentials.
  async function api(method, url, body) {
    const res = await fetch(url, {
      method, credentials: "same-origin",
      headers: Object.assign({ "X-CSRF-Token": csrf() }, body ? { "Content-Type": "application/json" } : {}),
      body: body ? JSON.stringify(body) : undefined,
    });
    if (res.status === 204) return null;
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error((data.error && data.error.message) || res.statusText);
    return data;
  }

  // Server-Sent Events helper: handlers keyed by event name; html payloads are
  // inserted into [data-stream-target] containers by the caller.
  function stream(url, handlers, opts) {
    opts = opts || {};
    let es, retry = 1000;
    function connect() {
      es = new EventSource(url, { withCredentials: true });
      es.onopen = () => { retry = 1000; document.body.classList.remove("stream-offline"); };
      es.onerror = () => { es.close(); document.body.classList.add("stream-offline"); setTimeout(connect, retry); retry = Math.min(retry * 2, 30000); };
      Object.keys(handlers).forEach((name) => es.addEventListener(name, (ev) => {
        let data = ev.data;
        try { data = JSON.parse(ev.data); } catch (e) { /* plain text */ }
        handlers[name](data, ev);
      }));
    }
    connect();
    return { close: () => es && es.close() };
  }

  // Escape text for safe HTML insertion when building rows from JSON.
  const esc = (s) => String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const f2 = (n) => (typeof n === "number" ? n.toFixed(2) : "");
  const hms = (iso) => { const d = new Date(iso); return isNaN(d) ? "" : d.toTimeString().slice(0, 8); };

  function eventRow(ev) {
    const tr = document.createElement("tr");
    tr.dataset.seq = ev.seq; tr.dataset.id = ev.id; tr.className = "cat-" + ev.category + " fresh";
    const evid = (ev.evidence && ev.evidence.length) ? ` <span title="${ev.evidence.length} evidence refs">◉${ev.evidence.length}</span>` : "";
    const cause = ev.causation_id ? `<a href="/events/${esc(ev.causation_id)}" title="caused by">↑</a>` : "";
    tr.innerHTML = `<td class="nowrap muted small mono" title="${esc(ev.occurred_at)}">${hms(ev.occurred_at)}</td>` +
      `<td><span class="etype cat-${esc(ev.category)}">${esc(ev.type)}</span></td>` +
      `<td class="small muted">${esc(ev.source)}</td>` +
      `<td class="summary"><a href="/events/${esc(ev.id)}">${esc(ev.summary)}</a></td>` +
      `<td class="num small">${f2(ev.salience)}</td><td class="num small">${f2(ev.confidence)}</td>` +
      `<td class="small muted nowrap">${cause}${evid}</td>`;
    return tr;
  }

  // Live event tables: <table data-stream="url" data-stream-after="seq"><tbody data-stream-target>
  function attachStreams() {
    document.querySelectorAll("table[data-stream]").forEach((table) => {
      const body = table.querySelector("[data-stream-target]");
      const status = document.querySelector("[data-stream-status]");
      const toggle = document.querySelector("[data-live-toggle]");
      let url = table.dataset.stream;
      const after = table.dataset.streamAfter;
      if (after && after !== "0") url += (url.includes("?") ? "&" : "?") + "after=" + encodeURIComponent(after);
      let handle = null;
      const start = () => {
        handle = stream(url, {
          cognitive_event: (ev) => {
            const empty = body.querySelector("[data-empty]"); if (empty) empty.remove();
            if (body.querySelector(`tr[data-seq="${ev.seq}"]`)) return;
            body.insertBefore(eventRow(ev), body.firstChild);
            while (body.children.length > 300) body.removeChild(body.lastChild);
            if (status) status.textContent = "live";
          },
        });
        if (status) status.textContent = "live";
      };
      const stop = () => { if (handle) handle.close(); handle = null; if (status) status.textContent = "paused"; };
      if (!toggle || toggle.checked) start();
      if (toggle) toggle.addEventListener("change", () => (toggle.checked ? start() : stop()));
    });
  }

  // Live sections: <div id="x" data-refresh-on="type1,type2" data-refresh-url="/page" data-stream="/api/.../stream?...">
  // On a matching event the page is re-fetched and the section swapped in place.
  function attachRefreshers() {
    document.querySelectorAll("[data-refresh-on][data-stream]").forEach((el) => {
      const types = el.dataset.refreshOn.split(",");
      const status = document.querySelector("[data-stream-status]");
      let timer = null;
      const refresh = () => fetch(el.dataset.refreshUrl, { credentials: "same-origin" }).then((r) => r.text()).then((html) => {
        const doc = new DOMParser().parseFromString(html, "text/html");
        const fresh = doc.getElementById(el.id);
        if (fresh) el.innerHTML = fresh.innerHTML;
      }).catch(() => {});
      stream(el.dataset.stream, {
        cognitive_event: (ev) => {
          if (status) status.textContent = "live";
          if (!types.includes(ev.type)) return;
          clearTimeout(timer); timer = setTimeout(refresh, 400);
        },
      });
      if (status) status.textContent = "live";
    });
  }

  document.addEventListener("DOMContentLoaded", () => {
    attachStreams();
    attachRefreshers();
    const tt = document.querySelector("[data-theme-toggle]");
    if (tt) tt.addEventListener("click", cycleTheme);
    const nt = document.querySelector("[data-nav-toggle]");
    const nav = document.querySelector("[data-nav]");
    if (nt && nav) nt.addEventListener("click", () => { const open = nav.classList.toggle("open"); nt.setAttribute("aria-expanded", String(open)); });

    // Confirmation on destructive buttons.
    document.addEventListener("click", (ev) => {
      const el = ev.target.closest("[data-confirm]");
      if (el && !window.confirm(el.getAttribute("data-confirm"))) { ev.preventDefault(); ev.stopImmediatePropagation(); }
    }, true);

    // Live value readouts for range inputs.
    document.querySelectorAll("input[type=range][data-out]").forEach((el) => {
      const out = el.parentElement.querySelector("output");
      if (out) el.addEventListener("input", () => (out.value = el.value));
    });

    // Auto-dismiss flashes.
    document.querySelectorAll(".flash").forEach((f) => setTimeout(() => f.remove(), 8000));

    // Keyboard: "t" toggles theme, "g d" dashboard, "g m" minds, "g s" system.
    let pending = "";
    document.addEventListener("keydown", (ev) => {
      if (ev.target.matches("input, textarea, select") || ev.metaKey || ev.ctrlKey || ev.altKey) return;
      if (ev.key === "t" && !pending) { cycleTheme(); return; }
      if (ev.key === "g") { pending = "g"; setTimeout(() => (pending = ""), 800); return; }
      if (pending === "g") {
        const map = { d: "/", m: "/minds", s: "/system", a: "/audit", l: "/llm", p: "/approvals" };
        if (map[ev.key]) location.href = map[ev.key];
        pending = "";
      }
    });
  });

  window.noema = { api, stream, csrf };
})();
