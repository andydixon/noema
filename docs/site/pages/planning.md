Plans turn intentions into explicit, revisable work: a directed acyclic graph of typed steps with assumptions, completion criteria, abort conditions, resource estimates and a budget. A language model may propose a decomposition; Go validates it, owns the state machine and invokes every capability through the single capability path. Plans survive restarts, are replanned as new revisions that keep their history, and treat an operator's attention as a scarce resource.

## Guarantees

- **Validated before use.** Unregistered or locked capabilities, cycles, missing dependencies and budget overruns are refused; fields in a proposal claiming approval are ignored.
- **Observe, act, verify.** A write step can carry a precondition and an expectation; the expectation is resolved by a later read-only observation, never assumed.
- **Resumable.** Steps are claimed with leases and saved conditionally, so a restart or a second executor cannot run a step twice or move it backwards.
- **Bounded host access.** Service, log and host capabilities run a fixed allow-list of commands with validated arguments and scrubbed output.

{{include docs/planning.md}}
