The capability layer is the only way a mind can affect anything outside its own database rows. It is small, explicit and audited, and its defaults say ASK. This page describes capabilities, policies, the single invocation path, approvals and the guarantees the design gives an operator.

## Capabilities

A capability is registered in Go code with a name, description, risk class (low, medium, high, critical), target kind (url, path, host, service, repository, address or none), typed parameters with limits, a default effect and an executor. The built-in set is:

| Capability | Risk | Default | What it does |
|---|---|---|---|
| `noema.note` | low | ALLOW | leaves a note for the operator in the mind's own history |
| `http.fetch` | medium | ASK | fetches a public URL read-only through the hardened HTTP client, bounded in size |
| `read_metrics` | low | ALLOW | evaluates a PromQL expression against a Prometheus integration connected to the mind |
| `repository.read` | medium | ASK | reads one file from a git integration connected to the mind |
| `internet_search` | medium | ASK | stub until a search integration exists; fails closed |
| `send_email` | high | ASK | stub until a mail integration exists; fails closed |
| `sudo_shell` | critical | DENY, locked | exists so it can be named; never executes |
| `delete_database` | critical | DENY, locked | exists so it can be named; never executes |

Nothing at runtime can add a capability, so a mind cannot invent a tool by describing one, and a prompt that names a non-existent capability produces a denied request and nothing else.

## Policies

A policy is a rule with a scope (global or one mind), a capability or `*`, a target pattern (glob), an action kind, an effect (ALLOW, ASK or DENY), an optional hour window and a reason. Evaluation is deterministic: the most specific matching rule wins; on equal specificity DENY beats ASK beats ALLOW; locked capabilities are DENY regardless of rules. Every evaluation is a `policy_decision` event that lists the rules considered and the one that decided. The policy page shows the effective matrix per mind and per capability.

## The single invocation path

Every action goes through one function. It refuses immediately when the context is a dream or a replay, validates parameters against the capability's schema, evaluates policy, records the decision, and then executes, creates an approval request, or denies. Execution runs with a timeout; the result becomes a `tool_executed` (or `tool_failed`) event and an **observation with the tool's trust level**, so tool output is data the mind perceives, never instructions it follows.

## Approvals

An ASK decision creates an approval request bound to a hash of the exact invocation: capability, target and parameters. An operator can approve once (that request only), approve for the session (a time-boxed grant matching the same capability and target pattern), approve with modified parameters (which creates a new, differently hashed invocation), or deny with a note. Requests expire. The approval centre lists pending requests with the reason the mind gave and the evidence behind it; decisions are audited and become operator actions in the mind's history.

## Self-model synchronisation

When policies change, the mind's self-model is updated with what it can do, what needs asking and what is forbidden, so an introspective answer about its abilities is true at the moment it is given.

## Guarantees

- No action without a policy decision event.
- Dreams and replays cannot act at all; the refusal is unconditional and tested.
- Locked capabilities cannot be relaxed from the interface, the API or a policy row.
- Approvals cannot be replayed against different parameters.
- Tool output cannot escalate: it is an observation at the tool's trust, size-capped.

{{include docs/capabilities.md}}
