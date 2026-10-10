#!/usr/bin/env python3
"""Build the Noema documentation section as static HTML.

Usage: python3 docs/site/build.py OUTPUT_DIR

Sources: the repository's Markdown documents, the OpenAPI document, the UI
screenshots, and the authored pages in docs/site/pages/*.md. A page file may
pull a repository document in with a line `{{include path [demote]}}`.
The output follows the dixon.cx page shell (css/site.css, navy rail) with the
full SEO head: indexable, canonical, Open Graph, Twitter and TechArticle JSON-LD.
Every page must also be listed in the Noema block of dixon.cx's sitemap.xml.
"""
import html
import json
import os
import re
import shutil
import sys

import markdown

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
PAGES = os.path.join(ROOT, "docs", "site", "pages")
BASE = "/noema/"
SITE = "https://dixon.cx"

# Order, slug, title, summary. The page body comes from pages/<slug>.md.
CHAPTERS = [
    ("index", "Noema", "A persistent, inspectable artificial cognitive runtime: minds that remember, reason with evidence, say how sure they are, and can always show their working."),
    ("concepts", "Concepts and vocabulary", "Minds, events, epistemic kinds, memory kinds, beliefs, drives and the interpretation boundary."),
    ("architecture", "Architecture", "The daemon, the cognitive cycle, the storage model and the boundaries between deterministic code and language models."),
    ("configuration", "Cognitive configuration", "The per-mind settings that shape a mind: workspace capacity, attention weights, budgets and background cognition."),
    ("personality", "Identity and personality", "Versioned personalities, templates, traits and how they modulate attention and voice."),
    ("cognition", "The cognitive cycle", "Processes, attention scoring, the global workspace, executive dispatch, modes and metacognition."),
    ("memory", "Memory", "Episodic, semantic, procedural and autobiographical memory; retrieval, decay, consolidation and provenance."),
    ("beliefs", "Beliefs and evidence", "Evidence-derived scores, what the numbers mean, statuses including unknown, temporal scope, source reliability, contradictions and operator corrections."),
    ("epistemics", "Epistemic honesty", "The 0.2 review: assertions before beliefs, evidence provenance, honest scores, unknowns, time, reliability, reproducible replay and redaction."),
    ("conversation", "Conversation", "How a message becomes an observation, a perception, a response intent and finally language."),
    ("motivation", "Goals, drives and affect", "The goal tree, the closed drive set, rule-driven affect and how they bias attention."),
    ("predictions", "Predictions and calibration", "Explicit predictions with criteria, deterministic resolution, prediction error and calibration."),
    ("graph", "Relationship graph", "Entities, evidenced edges, merges and the canvas explorer."),
    ("llm-providers", "Language models", "Roles, providers (OpenAI-compatible, Anthropic, Ollama, Amazon Bedrock), per-mind routing, budgets, recorded calls, replay and versioned prompts."),
    ("capabilities", "Capabilities and policy", "The only path to action: ALLOW / ASK / DENY, approvals, locked capabilities and tool output as data."),
    ("volition", "Volition", "Wants, intentions and commitments as distinct recorded states: generation, conflicts, formation, reconsideration and values."),
    ("planning", "Planning and execution", "Plans as validated graphs of typed steps, observe-act-verify, replanning, capability negotiation and bounded host operations."),
    ("agency", "Agency and why", "Authority outside cognition, the causal chain from want to outcome, attribution and forensic explanations."),
    ("integrations", "Integrations", "Research folder, git mirrors, Prometheus and signed webhooks as provenance-tagged observations."),
    ("dreaming", "Dreaming", "The opt-in idle mode: replay, association and speculative hypotheses that can never act."),
    ("snapshots-replay", "Snapshots, forks, replay and experiments", "Capturing state, branching minds, re-running cognition under a recorded runtime identity, and comparing forks that differ in one thing, including the model."),
    ("cognitive-research", "Cognitive research", "Operational definitions, ablations, introspection accuracy, calibration, agency metrics and the research dashboard, with no claim of consciousness."),
    ("ui-tour", "Interface tour", "Every screen of the web interface, what it shows and what an operator does there."),
    ("api", "HTTP API", "Authentication, roles, the error envelope, streaming and every route."),
    ("cli", "Command line", "What an operator can do from a terminal: every command, with examples."),
    ("security", "Security and threat model", "Trust boundaries, secret handling, sessions, two-factor authentication and the threat-by-threat verification table."),
    ("privacy", "Privacy and redaction", "Append-only history and the one audited path that removes content while keeping its shape."),
    ("decisions", "Architecture decision records", "The recorded decisions and the alternatives they rejected."),
    ("development", "How it was built", "The phased plan, what each phase delivered, measured performance, release notes and changelog."),
    ("glossary", "Glossary", "Short definitions of every term used across the documentation."),
]

STYLE = """
<style>
.noema-doc pre{background:rgba(127,127,127,.12);border:1px solid rgba(127,127,127,.25);border-radius:6px;padding:.9rem 1rem;overflow:auto;font-size:.85rem;line-height:1.45}
.noema-doc code{font-size:.9em;word-break:break-word}
.noema-doc pre code{word-break:normal;white-space:pre}
.noema-doc table{width:100%;border-collapse:collapse;margin:1rem 0;font-size:.92rem}
.noema-doc th,.noema-doc td{border:1px solid rgba(127,127,127,.3);padding:.45rem .6rem;vertical-align:top;text-align:left}
.noema-doc th{background:rgba(127,127,127,.12)}
.noema-doc .table-wrap{overflow-x:auto}
.noema-doc h3{font-size:1.05rem;margin-top:1.4rem}
.noema-doc h4{font-size:.95rem;margin-top:1.1rem;font-weight:600}
.noema-doc blockquote{border-left:3px solid rgba(127,127,127,.5);margin:1rem 0;padding:.2rem 1rem;opacity:.9}
.noema-doc figure{margin:1.2rem 0}.noema-doc figure img{width:100%;height:auto;border:1px solid rgba(127,127,127,.3);border-radius:6px}
.noema-doc figcaption{font-size:.85rem;opacity:.8;margin-top:.4rem}
.noema-doc .contents ol{columns:2;column-gap:2rem}@media(max-width:700px){.noema-doc .contents ol{columns:1}}
.noema-doc .method{display:inline-block;min-width:4.2rem;font-family:monospace;font-weight:700;font-size:.8rem}
.noema-doc .role{font-size:.78rem;opacity:.75;font-family:monospace}
.noema-doc dl dt{font-weight:600;margin-top:.6rem}.noema-doc dl dd{margin-left:0}
.noema-doc .section-box p+p,.noema-doc .section-box p+ul,.noema-doc .section-box p+ol,.noema-doc .section-box ul+p,.noema-doc .section-box ol+p,.noema-doc .section-box .table-wrap+p,.noema-doc .section-box pre+p{margin-top:.9rem}
.noema-doc .lead-ish{font-weight:600;opacity:.9;margin-bottom:1rem}
.noema-doc .hero{margin:.5rem 0 0}.noema-doc .hero-kicker{font-family:monospace;letter-spacing:.08em;text-transform:uppercase;font-size:.78rem;opacity:.7;margin:0 0 .4rem}
.noema-doc .hero-lead{font-size:1.15rem;line-height:1.55;margin:0 0 1rem}.noema-doc .hero-actions{display:flex;flex-wrap:wrap;gap:.5rem;margin:0}
.noema-doc .pillars{display:grid;grid-template-columns:repeat(2,1fr);gap:1rem}@media(max-width:760px){.noema-doc .pillars{grid-template-columns:1fr}}
.noema-doc .pillar{border:1px solid rgba(127,127,127,.3);border-radius:8px;padding:1rem 1.1rem}.noema-doc .pillar h3{margin:0 0 .5rem;font-size:1rem}.noema-doc .pillar p{margin:0;font-size:.95rem}
.noema-doc .shots{display:grid;grid-template-columns:repeat(2,1fr);gap:1rem}@media(max-width:760px){.noema-doc .shots{grid-template-columns:1fr}}.noema-doc .shots figure{margin:0}
.noema-doc .numbers{display:grid;grid-template-columns:repeat(3,1fr);gap:1rem;text-align:center}@media(max-width:760px){.noema-doc .numbers{grid-template-columns:repeat(2,1fr)}}
.noema-doc .numbers div{border:1px solid rgba(127,127,127,.3);border-radius:8px;padding:1rem .6rem}.noema-doc .numbers b{display:block;font-size:1.9rem;line-height:1.1}.noema-doc .numbers span{display:block;font-size:.82rem;opacity:.8;margin-top:.3rem}
</style>
"""

HEAD = """<!doctype html>
<html lang="en-GB">
	<head>
		<meta charset="utf-8">
		<meta name="viewport" content="width=device-width, initial-scale=1">
		<meta name="description" content="{desc}">
		<meta name="author" content="Andy Dixon">
		<meta name="robots" content="index, follow">
		<link rel="canonical" href="{canonical}">
		<link rel="alternate" type="application/atom+xml" title="Andy Dixon: Blog" href="/blog/feed.xml">
		<title>{titletag} | Andy Dixon</title>
		<meta property="og:type" content="article">
		<meta property="og:title" content="{ogtitle}">
		<meta property="og:description" content="{desc}">
		<meta property="og:url" content="{canonical}">
		<meta property="og:image" content="https://dixon.cx/assets/images/og-home.png">
		<meta property="og:image:width" content="1200">
		<meta property="og:image:height" content="630">
		<meta property="og:image:alt" content="Andy Dixon: software engineer, applied AI and security">
		<meta property="og:site_name" content="Andy Dixon">
		<meta property="og:locale" content="en_GB">
		<meta name="twitter:card" content="summary_large_image">
		<meta name="twitter:title" content="{ogtitle}">
		<meta name="twitter:description" content="{desc}">
		<meta name="twitter:image" content="https://dixon.cx/assets/images/og-home.png">
		<script type="application/ld+json">
{jsonld}
		</script>
		{style}
		<link href="/assets/images/favicon.png" rel="icon" type="image/png">
		<link rel="preconnect" href="https://fonts.googleapis.com">
		<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
		<link href="https://fonts.googleapis.com/css2?family=Poppins:wght@700;800&family=Inter:wght@400;500;600&display=swap" rel="stylesheet">
		<link href="/css/site.css?v=20260922c" rel="stylesheet">
	</head>
	<body class="inner">
		<a class="skip" href="#main">Skip to content</a>
		<div class="shell">
			<aside class="side">
				<a class="brand" href="/">Andy <br><span>Dixon</span></a>
				<nav class="side-nav" aria-label="Main">
					<a href="/">Home</a>
					<a href="/#portfolio">Work</a>
					<a href="/about">About</a>
					<a href="/blog">Blog</a>
					<a href="/noema/" aria-current="page">Noema</a>
					<a href="/privacy-guide/">Privacy Guide</a>
					<a href="/digital-forensics-guide/">Forensics Guide</a>
					<a href="/qualifications">Qualifications</a>
					<a href="/side-quests">Side Quests</a>
					<a href="/contact">Contact</a>
				</nav>
				<p class="side-foot">Software engineer, applied AI and security. Remote, worldwide.<br><a href="mailto:web@dixon.cx">web@dixon.cx</a> <a href="https://github.com/andydixon">GitHub</a></p>
			</aside>
			<main id="main" class="content">
				<div class="sections-wrapper noema-doc">

"""

TAIL = """
				</div>
				<footer class="site-footer">
					<p>Andy Dixon. <a href="https://github.com/andydixon">GitHub</a> <a href="/privacy">Privacy</a> <a href="/blog/feed.xml">Atom feed</a></p>
				</footer>
			</main>
		</div>
		<script src="/js/site.js"></script>
	</body>
</html>
"""

# dixon.cx house style: plain ASCII punctuation on every published page.
PLAIN = str.maketrans({"\u2014": "-", "\u2013": "-", "\u2018": "'", "\u2019": "'", "\u201c": '"', "\u201d": '"', "\u2026": "..."})

LINK_MAP = {
    "ARCHITECTURE.md": "architecture", "SECURITY.md": "security", "THREAT_MODEL.md": "security", "PLAN.md": "development",
    "CONTRIBUTING.md": "development", "CHANGELOG.md": "development", "README.md": "index",
    "docs/interpretation.md": "concepts", "docs/configuration.md": "configuration", "docs/ENVIRONMENT.md": "configuration",
    "docs/personality.md": "personality", "docs/cognition.md": "cognition", "docs/memory.md": "memory", "docs/beliefs.md": "beliefs",
    "docs/conversation.md": "conversation", "docs/motivation.md": "motivation", "docs/predictions.md": "predictions", "docs/graph.md": "graph",
    "docs/llm-providers.md": "llm-providers", "docs/capabilities.md": "capabilities", "docs/integrations.md": "integrations",
    "docs/dreaming.md": "dreaming", "docs/snapshots-replay.md": "snapshots-replay", "docs/performance.md": "development",
    "docs/RELEASE_NOTES.md": "development", "docs/privacy.md": "privacy", "docs/epistemics.md": "epistemics", "docs/volition.md": "volition", "docs/planning.md": "planning", "docs/agency.md": "agency", "docs/cognitive-research.md": "cognitive-research", "docs/openapi.json": "api", "docs/adr": "decisions",
}


def read(path):
    with open(os.path.join(ROOT, path), encoding="utf-8") as f:
        return f.read()


def demote(md, levels):
    out = []
    fence = False
    for line in md.split("\n"):
        if line.startswith("```"):
            fence = not fence
        if not fence and line.startswith("#"):
            line = "#" * levels + line
        out.append(line)
    return "\n".join(out)


def strip_h1(md):
    lines = md.split("\n")
    for i, l in enumerate(lines):
        if l.startswith("# "):
            return "\n".join(lines[:i] + lines[i + 1:])
    return md


def expand(md):
    """Resolve {{include path [demote N] [keep-title]}} and {{openapi}} directives."""
    def inc(m):
        parts = m.group(1).split()
        path = parts[0]
        body = read(path)
        d = 0
        keep = False
        for p in parts[1:]:
            if p.startswith("demote"):
                d = int(p.split("=")[1]) if "=" in p else 1
            if p == "keep-title":
                keep = True
        if not keep:
            body = strip_h1(body)
        if d:
            body = demote(body, d)
        return body
    md = re.sub(r"\{\{include ([^}]+)\}\}", inc, md)
    md = md.replace("{{openapi}}", openapi_markdown())
    return md


def rewrite_links(html_text):
    def fix(m):
        href = m.group(1)
        if href.startswith(("http://", "https://", "#", "mailto:", "/")):
            return m.group(0)
        anchor = ""
        if "#" in href:
            href, anchor = href.split("#", 1)
            anchor = "#" + anchor
        if href.startswith("docs/screenshots/"):
            return 'href="' + BASE + "img/" + os.path.basename(href) + '"'
        for k, v in LINK_MAP.items():
            if href == k or href.rstrip("/") == k or href.startswith(k + "/"):
                return 'href="' + BASE + v + anchor + '"'
        if href.endswith(".md"):
            slug = os.path.splitext(os.path.basename(href))[0]
            return 'href="' + BASE + slug + anchor + '"'
        return m.group(0)
    html_text = re.sub(r'href="([^"]+)"', fix, html_text)
    html_text = re.sub(r'src="docs/screenshots/([^"]+)"', r'src="' + BASE + r'img/\1"', html_text)
    return html_text


def openapi_markdown():
    doc = json.loads(read("internal/api/openapi.json"))
    groups = {}
    for path, ops in doc["paths"].items():
        for method, op in ops.items():
            tag = (op.get("tags") or ["root"])[0]
            groups.setdefault(tag, []).append((path, method.upper(), op))
    out = []
    for tag in sorted(groups):
        out.append(f"\n### {tag}\n")
        out.append("| Method | Path | Minimum role | Operation |\n|---|---|---|---|")
        for path, method, op in sorted(groups[tag], key=lambda x: (x[0], x[1])):
            role = str(op.get("x-required-role", "")).replace("(", "").replace(")", "")
            summary = op.get("summary", op.get("operationId", ""))
            out.append(f"| `{method}` | `{path}` | {role} | {summary} |")
    return "\n".join(out) + "\n"


def render_page(idx, slug, title, summary, md):
    md = expand(md)
    conv = markdown.Markdown(extensions=["tables", "fenced_code", "toc", "sane_lists", "attr_list", "def_list", "md_in_html"], extension_configs={"toc": {"toc_depth": "2-3"}})
    body = conv.convert(md)
    body = rewrite_links(body)
    body = re.sub(r"<table>", '<div class="table-wrap"><table>', body).replace("</table>", "</table></div>")
    # Split into section boxes at h2 boundaries.
    parts = re.split(r"(?=<h2 )", body)
    intro = parts[0]
    sections = parts[1:]
    headings = []
    boxes = []
    for sec in sections:
        m = re.match(r'<h2 id="([^"]+)">(.*?)</h2>', sec, re.S)
        if not m:
            boxes.append('<div class="section-box mt-4">' + sec + "</div>")
            continue
        hid, htext = m.group(1), m.group(2)
        headings.append((hid, re.sub("<[^>]+>", "", htext)))
        rest = sec[m.end():]
        boxes.append(f'<article class="section-box mt-4" id="{hid}"><h2 class="mono-heading h6 mb-3">{htext}</h2>{rest}</article>')
    contents = ""
    if headings:
        contents = '<h2 class="mono-heading h6 mt-4 mb-2">On this page</h2><div class="contents"><ol class="mb-0">' + "".join(
            f'<li><a href="#{h}">{t}</a></li>' for h, t in headings) + "</ol></div>"
    prev_link = next_link = ""
    if idx > 0:
        ps, pt, _ = CHAPTERS[idx - 1]
        prev_link = f'<a class="button button-sm button-outline" href="{BASE}{"" if ps == "index" else ps}">&larr; {html.escape(pt)}</a>'
    if idx + 1 < len(CHAPTERS):
        ns, nt, _ = CHAPTERS[idx + 1]
        next_link = f'<a class="button button-sm button-outline" href="{BASE}{ns}">{html.escape(nt)} &rarr;</a>'
    nav_box = f'''<div class="section-box mt-4"><div class="d-flex flex-wrap gap-2 justify-content-between">{prev_link or "<span></span>"}<a class="button button-sm button-outline" href="{BASE}">All sections</a>{next_link or "<span></span>"}</div></div>'''
    head_box = f'''<div class="section-box">
							<h6 class="title-heading mb-3" data-backdrop-text="Noema"><a class="link-decoration" href="{BASE}">Noema documentation</a></h6>
							<h1>{html.escape(title)}</h1>
							<p class="mt-3 lead-ish">{html.escape(summary)}</p>
							{intro}
							{contents}
						</div>'''
    canonical = SITE + BASE + ("" if slug == "index" else slug)
    ld = {"@context": "https://schema.org", "@type": "TechArticle", "headline": title, "description": summary,
          "author": {"@type": "Person", "name": "Andy Dixon", "url": SITE + "/"},
          "publisher": {"@type": "Person", "name": "Andy Dixon", "url": SITE + "/"},
          "mainEntityOfPage": canonical, "inLanguage": "en-GB"}
    if slug != "index":
        ld["isPartOf"] = {"@type": "CreativeWork", "name": "Noema", "url": SITE + BASE}
    jsonld = "\n".join("\t\t" + l for l in json.dumps(ld, indent=2, ensure_ascii=False).replace("</", "<\\/").split("\n"))
    titletag = title if slug == "index" else title + " | Noema"
    ogtitle = "Noema | Andy Dixon" if slug == "index" else title + " | Noema"
    page = HEAD.format(titletag=html.escape(titletag), ogtitle=html.escape(ogtitle), desc=html.escape(summary), canonical=canonical, jsonld=jsonld, style=STYLE) + head_box + "\n".join(boxes) + nav_box + TAIL
    return page


def main():
    if len(sys.argv) != 2:
        sys.exit("usage: build.py OUTPUT_DIR")
    out = sys.argv[1]
    os.makedirs(os.path.join(out, "img"), exist_ok=True)
    for f in os.listdir(os.path.join(ROOT, "docs", "screenshots")):
        if f.endswith(".png"):
            shutil.copy(os.path.join(ROOT, "docs", "screenshots", f), os.path.join(out, "img", f))
    shutil.copy(os.path.join(ROOT, "internal", "api", "openapi.json"), os.path.join(out, "openapi.json"))
    shutil.copy(os.path.join(ROOT, "docs", "site", "htaccess"), os.path.join(out, ".htaccess"))
    for i, (slug, title, summary) in enumerate(CHAPTERS):
        with open(os.path.join(PAGES, slug + ".md"), encoding="utf-8") as f:
            md = f.read()
        page = render_page(i, slug, title, summary, md).translate(PLAIN)
        with open(os.path.join(out, slug + ".html"), "w", encoding="utf-8") as f:
            f.write(page)
        print("wrote", slug + ".html", len(page) // 1024, "KB")


if __name__ == "__main__":
    main()
