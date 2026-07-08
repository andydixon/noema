<div class="hero">
<p class="hero-kicker">An artificial cognitive runtime</p>
<p class="hero-lead">Most AI systems answer. <strong>Noema thinks, remembers, doubts and can show its working.</strong> It is a persistent runtime that hosts minds: long-lived cognitive entities with their own identity, memories, beliefs with evidence, goals, drives and an unbroken record of every thought they have ever had. Language models are plug-in services it calls when it needs language. The mind is the thing that stays.</p>
<p class="hero-actions"><a class="button button-sm" href="/noema/getting-started">Run it in ten minutes</a> <a class="button button-sm button-outline" href="/noema/ui-tour">See every screen</a> <a class="button button-sm button-outline" href="/noema/architecture">Read the architecture</a></p>
</div>

## Why it is different

<div class="pillars">
<div class="pillar"><h3>It never loses the thread</h3><p>A mind runs whether or not anyone is talking to it. Messages, documents, commits, metrics and webhooks arrive as events; the mind perceives them, recalls what is relevant, forms and revises beliefs, and picks up exactly where it left off after a restart. There is no context window to fall off the end of.</p></div>
<div class="pillar"><h3>Confidence is earned, not asserted</h3><p>Every proposition says what kind of knowledge it is. A claim stays a claim until independent evidence arrives, confidence is computed by a formula you can read on the page, and one source repeating itself is discounted. A Noema mind says <em>I do not know</em> because the arithmetic tells it to.</p></div>
<div class="pillar"><h3>You can always ask why</h3><p>Every thought that reached the workspace carries its attention score and its parents. Every memory carries its source and trust. Every reply carries the beliefs it rests on. Every model call is recorded with its prompt version. Nothing in the history can be edited, by anyone, including the mind.</p></div>
<div class="pillar"><h3>It cannot act behind your back</h3><p>The only path to the outside world is a capability layer with ALLOW, ASK and DENY policies, approvals bound to the exact request, and two capabilities locked shut forever. Dreams and replays cannot act at all. There is no self-preservation drive; shutdown is always yours.</p></div>
<div class="pillar"><h3>The model is replaceable</h3><p>Perception, hypothesis, summary, judgement, narrative and response are roles. Point a role at OpenAI, Anthropic, a local Ollama or nothing at all: the deterministic core keeps working, the mind keeps its identity, memories and beliefs, and recorded calls make model-dependent steps reproducible.</p></div>
<div class="pillar"><h3>Built to be studied</h3><p>Snapshot a mind, fork it, change one thing, and compare. Replay its recorded cognition and see, step by step, whether the result was exact, approximate or diverged. Run controlled experiments with stimulus scripts and expectations. This is a laboratory, not a chatbot.</p></div>
</div>

## What you see

<figure><img src="img/thoughts.png" alt="The thought stream of a running mind"><figcaption>The thought stream: observations, recollections with score breakdowns, thought candidates, the winners, and what the executive did about them, live.</figcaption></figure>

<div class="shots">
<figure><img src="img/beliefs.png" alt="Beliefs with evidence"><figcaption>Beliefs with their evidence, statuses and the formula behind each confidence.</figcaption></figure>
<figure><img src="img/replay.png" alt="Cognitive replay"><figcaption>Replay: recorded cognition re-run in a sandbox and compared honestly.</figcaption></figure>
<figure><img src="img/graph.png" alt="Relationship graph"><figcaption>The relationship graph, every edge backed by an event.</figcaption></figure>
<figure><img src="img/policies.png" alt="Capabilities and policy"><figcaption>Capabilities and policy: the only way a mind can act.</figcaption></figure>
</div>

## Inside a mind

Every cycle is the same deterministic sequence. New events are read. A bounded set of cognitive processes (attention, retrieval, association, perception, goal monitoring, curiosity, metacognition, action) each propose thought candidates. Ten inspectable features score every candidate; the top few enter a capacity-limited workspace; the executive acts on what won. Motivation and narrative update, the transaction commits, and only then does anyone see the new events.

Around that loop sit the long-lived structures: four kinds of memory with fused, explainable retrieval; beliefs with evidence arithmetic and contradiction detection; a goal tree, a closed set of drives and a small affect vector that bias attention within bounds; predictions with deterministic resolution and a calibration record; a relationship graph; a self-model that knows exactly which capabilities policy currently grants. Behind them, an append-only event store the database itself refuses to alter, and a scheduler that runs consolidation, reflection and, if you switch it on, dreaming.

```
 stimuli ──▶ perception ──▶ events (append-only) ──▶ per-mind runtime
                                                         │
             processes ◀─────────────────────────────────┘
             attention scoring ──▶ workspace ──▶ executive
                                                   │
             capability + policy layer (ALLOW / ASK / DENY) ──▶ tools
```

## By the numbers

<div class="numbers">
<div><b>34 700</b><span>lines of Go, standard library first, three dependencies</span></div>
<div><b>47</b><span>cognitive event types, all append-only</span></div>
<div><b>162</b><span>documented API operations, coverage-tested</span></div>
<div><b>31</b><span>command-line commands</span></div>
<div><b>16</b><span>phases from plan to release, each committed with tests</span></div>
<div><b>0</b><span>findings from the race detector, staticcheck, gosec and govulncheck at release</span></div>
</div>

## Designed for a world that lies to it

Noema assumes every external byte is hostile. Prompt injection is a threat model with a test corpus: instructions inside messages, documents, commits, metric labels or tool output are data, never orders. Secrets are references encrypted under a master key and are never rendered back. Sessions have CSRF protection, two-factor authentication and revocation. Two minds on one daemon cannot see each other's state, and a test proves it. The [threat model](security) lists every threat, its mitigation, and the test that verifies it.

## Who it is for

- **Researchers** who want a reproducible substrate for studying attention, memory, belief revision and motivation, with replay and experiments built in.
- **Engineers** who want an assistant that remembers their infrastructure, notices what changed, tells them how sure it is, and asks before it touches anything.
- **Anyone tired of confident nonsense** who would rather have a system that knows the difference between what it was told, what it inferred and what it verified.

## What it is not

Noema does not claim consciousness, sentience or personhood. Its components borrow names from cognitive science because those names describe what each part does; they are data structures and deterministic rules, sometimes assisted by a model. That honesty is a feature: it is what lets you trust the rest. The [interpretation boundary](concepts#the-interpretation-boundary) is part of the specification.

## Start here

1. [Getting started](getting-started): database, binary, first mind, first conversation.
2. [Concepts](concepts): the vocabulary in one page.
3. [Interface tour](ui-tour): every screen with screenshots.
4. [Capabilities](capabilities): how to let a mind do anything, safely.
5. [Snapshots, forks, replay and experiments](snapshots-replay): the laboratory.
