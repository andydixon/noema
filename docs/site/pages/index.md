Noema is a persistent, inspectable artificial cognitive runtime. It is a daemon, written in Go and backed by PostgreSQL, that hosts one or more *minds*: long-lived cognitive entities with their own identity, personality, memories, beliefs, goals, drives, affective state and an append-only history of everything they have ever thought. This documentation describes what Noema is, how it is built, how to run it, and how to read what it does.

## What Noema is

A mind in Noema is not a chat session. It runs continuously whether or not anyone is talking to it. Events arrive from many sources: a message from an operator, a file dropped in a watched folder, a commit in a mirrored repository, a metric crossing a threshold, a signed webhook, or the mind's own background processes. Each event is recorded, perceived, and offered to a set of cognitive processes that compete for a small, capacity-limited **workspace**. Whatever wins that competition is what the mind is "thinking about", and the executive layer acts on it: encoding memories, forming or revising beliefs, updating goals, making predictions, and, only through a policy-governed capability layer, doing things in the world.

The result is a system you can ask *why*. Every belief points at the evidence that produced its confidence and the arithmetic that combined it. Every memory carries its source, trust level and the event that created it. Every thought that reached the workspace is an event with a score breakdown. Every model call is recorded with its prompt version and can be replayed. Snapshots capture the whole state; forks branch it; replays re-run recorded cognition and say honestly whether the result was exact, approximate or diverged.

## What Noema is not

Noema does not claim consciousness, sentience, self-awareness or personhood, and its documentation never will. Components are named after psychological concepts (attention, working memory, emotion-like state, curiosity, drives, self-model, reflection, dreaming) because those names describe the functional role each plays. They are ordinary data structures and deterministic rules, sometimes assisted by language models. Behaviour that resembles emotion or preference is a property of the mechanism being run, not evidence of experience. The [concepts page](concepts) states the interpretation boundary in full.

Noema is also not a language model with a memory bolted on. Language models are **cognitive service providers**: they understand a message into structure, propose hypotheses, summarise, judge equivalence, or turn a response *intent* into prose. They are replaceable per role and per model. A mind keeps its identity, memories, beliefs and configuration when the provider changes, and the deterministic core keeps working with no model configured at all.

## Design principles

- **Evidence over appearance.** Every stored proposition carries an epistemic kind: observation, fact, memory, claim, hypothesis, belief, prediction, assumption, inference or opinion. A claim is never silently promoted to a fact.
- **Provenance everywhere.** Memories, beliefs, entities and responses point back at the events and sources that produced them; the event store is append-only and the database refuses updates and deletes on it.
- **Explicit uncertainty.** Confidence is computed from evidence with an inspectable formula, bounded away from certainty, and a mind is expected to say "I do not know".
- **Reproducibility.** Deterministic scoring with stable tie-breaks, recorded model calls, snapshots and replay make cognition re-runnable and differences visible.
- **Least privilege.** A mind can only act through the capability layer. Policies are ALLOW, ASK or DENY; two capabilities are locked to DENY and cannot be relaxed from any interface.
- **Human agency.** There is no self-preservation drive, no replication, no permission-seeking loop. Shutdown, pausing, resetting and forking are always operator decisions.
- **Hostile input.** Every external byte is data. Prompt injection is a threat model with a test corpus, not a hope.

## How the documentation is organised

The pages read in order as a book but stand alone as reference. Start with [Concepts](concepts) for the vocabulary, [Architecture](architecture) for the shape of the system and [Getting started](getting-started) to run one. The subsystem pages ([memory](memory), [beliefs](beliefs), [cognition](cognition), [capabilities](capabilities) and so on) each explain the mechanism, what an operator can inspect, and where the deterministic code ends and a model begins. The [interface tour](ui-tour) walks every screen with screenshots. [API](api) and [command line](cli) are complete references. [Security](security) carries the threat model with the tests that verify each mitigation. [Operations](operations) covers deployment and upkeep, and [Development](development) records how the system was built, phase by phase.

## Status

The documentation describes Noema 0.1.0: every phase of the original plan is implemented, tested with the race detector, checked with staticcheck, gosec and govulncheck, and released. The source is a private repository; the release notes and changelog are reproduced under [Development](development).
