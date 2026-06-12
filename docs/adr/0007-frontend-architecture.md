# ADR-0007: Server-rendered UI with dependency-free enhancement

Date: 2026-09-14  
Status: Accepted

## Context
The UI must be a rich cognitive observatory yet maintainable by Go developers, secure against XSS, and free of supply-chain risk.

## Decision
Go `html/template` pages embedded in the binary, one layout, CSS custom properties with light/dark themes (system preference plus toggle), and a small amount of hand-written vanilla JavaScript: an SSE client that inserts server-rendered HTML fragments, form enhancements (fetch + swap where it helps, plain POST-redirect-GET otherwise), keyboard navigation, and a canvas force-directed graph for the relationship explorer. Charts are inline SVG rendered by templates. No npm, no bundler, no frameworks, no CDN. Strict CSP with no inline scripts.

## Consequences
- Every view is a Go handler + template; live updates reuse the same templates.
- Graph rendering is bounded (node caps, filters) by design.
- If a page later needs heavier interactivity, a single vendored, hash-pinned library can be added under `web/static/vendor/` with an ADR update.

## Alternatives considered
- React/Vue SPA: doubles the codebase and toolchain for little gain in an operator tool.
- HTMX: close to what is needed, but a 30-line SSE/fragment helper covers our usage without a third-party script.
