/* Relationship explorer: a small force-directed layout on canvas. No library.
   Data comes from the graph API as {nodes:[{id,name,kind,mention_count}], edges:[{from_entity,to_entity,relation,confidence,count}]}.
   Bounded by the server (node cap), so the simulation stays cheap. */
(function () {
  "use strict";
  const KIND_COLOURS = { person: "#2f6fed", system: "#0ea5a4", project: "#16a34a", document: "#d97706", concept: "#8b5cf6", repository: "#64748b", organisation: "#db2777", place: "#0891b2", other: "#9ca3af" };

  function explorer(canvas, opts) {
    const ctx = canvas.getContext("2d");
    let nodes = [], edges = [], byId = {}, selected = null, hover = null, dragging = null, running = true;
    let scale = 1, ox = 0, oy = 0;
    const dpr = window.devicePixelRatio || 1;

    function resize() {
      const w = canvas.clientWidth, h = canvas.clientHeight;
      canvas.width = w * dpr; canvas.height = h * dpr; ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    }
    function load(data) {
      const prev = byId; byId = {};
      nodes = (data.nodes || []).map((n, i) => {
        const p = prev[n.id];
        const angle = (i / Math.max(1, data.nodes.length)) * Math.PI * 2;
        const node = Object.assign({}, n, { x: p ? p.x : canvas.clientWidth / 2 + Math.cos(angle) * 120, y: p ? p.y : canvas.clientHeight / 2 + Math.sin(angle) * 120, vx: 0, vy: 0, r: 6 + Math.min(14, Math.sqrt(n.mention_count || 1) * 2) });
        byId[n.id] = node; return node;
      });
      edges = (data.edges || []).filter((e) => byId[e.from_entity] && byId[e.to_entity]);
      running = true; energy = 1;
    }
    let energy = 1;
    function step() {
      if (!running) return;
      const w = canvas.clientWidth, h = canvas.clientHeight;
      // repulsion
      for (let i = 0; i < nodes.length; i++) for (let j = i + 1; j < nodes.length; j++) {
        const a = nodes[i], b = nodes[j]; let dx = b.x - a.x, dy = b.y - a.y; let d2 = dx * dx + dy * dy + 0.01; const d = Math.sqrt(d2);
        const f = 1800 / d2; dx /= d; dy /= d; a.vx -= dx * f; a.vy -= dy * f; b.vx += dx * f; b.vy += dy * f;
      }
      // springs
      for (const e of edges) {
        const a = byId[e.from_entity], b = byId[e.to_entity]; const dx = b.x - a.x, dy = b.y - a.y; const d = Math.sqrt(dx * dx + dy * dy) + 0.01;
        const target = 90 - 20 * Math.min(1, (e.count || 1) / 5); const f = (d - target) * 0.02; a.vx += (dx / d) * f; a.vy += (dy / d) * f; b.vx -= (dx / d) * f; b.vy -= (dy / d) * f;
      }
      // gravity to centre, integrate
      let motion = 0;
      for (const n of nodes) {
        if (n === dragging) continue;
        n.vx += (w / 2 - n.x) * 0.002; n.vy += (h / 2 - n.y) * 0.002; n.vx *= 0.82; n.vy *= 0.82; n.x += n.vx * energy; n.y += n.vy * energy; motion += Math.abs(n.vx) + Math.abs(n.vy);
      }
      energy = Math.max(0.05, energy * 0.995);
      if (motion < 0.02 * nodes.length) running = false;
    }
    function draw() {
      const w = canvas.clientWidth, h = canvas.clientHeight; ctx.clearRect(0, 0, w, h);
      ctx.save(); ctx.translate(ox, oy); ctx.scale(scale, scale);
      const cs = getComputedStyle(document.documentElement);
      const muted = cs.getPropertyValue("--muted").trim() || "#888", text = cs.getPropertyValue("--text").trim() || "#111", surface = cs.getPropertyValue("--surface").trim() || "#fff";
      for (const e of edges) {
        const a = byId[e.from_entity], b = byId[e.to_entity]; ctx.strokeStyle = muted; ctx.globalAlpha = 0.25 + 0.6 * (e.confidence || 0.3); ctx.lineWidth = 1 + Math.min(3, (e.count || 1) / 3);
        ctx.beginPath(); ctx.moveTo(a.x, a.y); ctx.lineTo(b.x, b.y); ctx.stroke();
        if (scale > 0.9 && (e === hoverEdge || selected && (selected.id === a.id || selected.id === b.id))) {
          ctx.globalAlpha = 1; ctx.fillStyle = muted; ctx.font = "10px sans-serif"; ctx.fillText(e.relation.replace(/_/g, " "), (a.x + b.x) / 2 + 4, (a.y + b.y) / 2 - 4);
        }
      }
      ctx.globalAlpha = 1;
      for (const n of nodes) {
        ctx.beginPath(); ctx.arc(n.x, n.y, n.r, 0, Math.PI * 2); ctx.fillStyle = KIND_COLOURS[n.kind] || KIND_COLOURS.other; ctx.fill();
        if (n === selected || n === hover) { ctx.lineWidth = 3; ctx.strokeStyle = text; ctx.stroke(); }
        else { ctx.lineWidth = 1.5; ctx.strokeStyle = surface; ctx.stroke(); }
        ctx.fillStyle = text; ctx.font = (n === selected ? "bold " : "") + "12px sans-serif"; ctx.fillText(n.name, n.x + n.r + 4, n.y + 4);
      }
      ctx.restore();
    }
    let hoverEdge = null;
    function loop() { step(); draw(); requestAnimationFrame(loop); }
    function toWorld(ev) { const rect = canvas.getBoundingClientRect(); return { x: (ev.clientX - rect.left - ox) / scale, y: (ev.clientY - rect.top - oy) / scale }; }
    function nodeAt(p) { for (let i = nodes.length - 1; i >= 0; i--) { const n = nodes[i]; if ((p.x - n.x) ** 2 + (p.y - n.y) ** 2 <= (n.r + 4) ** 2) return n; } return null; }
    canvas.addEventListener("mousemove", (ev) => { const p = toWorld(ev); if (dragging) { dragging.x = p.x; dragging.y = p.y; running = true; energy = Math.max(energy, 0.3); return; } hover = nodeAt(p); canvas.style.cursor = hover ? "pointer" : "grab"; });
    canvas.addEventListener("mousedown", (ev) => { const p = toWorld(ev); dragging = nodeAt(p); if (!dragging) { panStart = { x: ev.clientX - ox, y: ev.clientY - oy }; } });
    let panStart = null;
    window.addEventListener("mousemove", (ev) => { if (panStart) { ox = ev.clientX - panStart.x; oy = ev.clientY - panStart.y; } });
    window.addEventListener("mouseup", () => { dragging = null; panStart = null; });
    canvas.addEventListener("click", (ev) => { const n = nodeAt(toWorld(ev)); selected = n; if (opts.onSelect) opts.onSelect(n); });
    canvas.addEventListener("dblclick", (ev) => { const n = nodeAt(toWorld(ev)); if (n && opts.onFocus) opts.onFocus(n); });
    canvas.addEventListener("wheel", (ev) => { ev.preventDefault(); const f = ev.deltaY < 0 ? 1.1 : 0.9; scale = Math.min(3, Math.max(0.3, scale * f)); }, { passive: false });
    window.addEventListener("resize", resize);
    resize(); loop();
    return { load, select: (id) => { selected = byId[id] || null; }, colours: KIND_COLOURS };
  }
  window.noemaGraph = { explorer };

  document.addEventListener("DOMContentLoaded", () => {
    const canvas = document.querySelector("canvas[data-graph]");
    if (!canvas) return;
    const panel = document.getElementById("entity-panel");
    const form = document.querySelector("[data-graph-filter]");
    const base = canvas.dataset.graph;
    const mindSlug = canvas.dataset.mind;
    const ex = explorer(canvas, {
      onSelect: (n) => { if (!n) { panel.innerHTML = '<p class="muted">Click a node.</p>'; return; } fetchEntity(n.id); },
      onFocus: (n) => { if (n) { const u = new URL(location.href); u.searchParams.set("focus", n.id); location.href = u.toString(); } },
    });
    function fetchEntity(id) {
      fetch(`/minds/${mindSlug}/entities/${id}?partial=1`, { credentials: "same-origin" }).then((r) => r.text()).then((html) => { panel.innerHTML = html; }).catch(() => { panel.textContent = "Could not load entity."; });
    }
    function loadGraph() {
      const params = new URLSearchParams(form ? new FormData(form) : {});
      fetch(base + (params.toString() ? "?" + params.toString() : ""), { credentials: "same-origin" }).then((r) => r.json()).then((data) => {
        ex.load(data);
        const info = document.querySelector("[data-graph-info]");
        if (info) info.textContent = `${data.nodes.length} of ${data.total_entities} entities · ${data.edges.length} relationships`;
        const focus = params.get("focus"); if (focus) { ex.select(focus); fetchEntity(focus); }
      });
    }
    if (form) form.addEventListener("change", loadGraph);
    loadGraph();
  });
})();
