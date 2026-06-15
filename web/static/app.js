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

  document.addEventListener("DOMContentLoaded", () => {
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
