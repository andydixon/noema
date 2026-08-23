# ADR-0009: Redaction as the only write on append-only history

Date: 2026-09-15  
Status: Accepted

## Context
ADR-0002 made cognitive history append-only and promised a "dedicated, audited maintenance path" for removal. Privacy law and ordinary decency require that personal content can be made irrecoverable, while provenance, replay and forensic reconstruction require that the *shape* of history survives.

## Decision
Content is redacted, rows are never deleted. A `redactions` ledger (append-only) records target, reason, policy, requester and a SHA-256 of the removed content. One SQL function, `noema_redact()`, performs the replacement under a transaction-local flag that the append-only trigger admits for UPDATE only; the function is `SECURITY DEFINER`, revoked from `PUBLIC`, and the application never sets the flag. Content columns are replaced by a fixed marker; identifiers, types, timestamps, links, scores and breakdowns are kept. A `redaction` event is appended. Cascade follows recorded derivations one level (episodes, messages, model calls, assertions, beliefs rooted at the event, and the events that quote each of them). Stored snapshot archives are scrubbed and re-signed.

## Consequences
- Irrecoverability covers everything the system controls; backups, exports and replicas made earlier are the operator's responsibility and are documented as such.
- History keeps its structure: replay labels redacted stimuli instead of inventing them; belief provenance remains inspectable without its text.
- The append-only guarantee is weakened by exactly one, auditable, content-only write path.

## Alternatives considered
- Cryptographic erasure (per-row keys destroyed on request): the strongest option, rejected for now because search, retrieval, contradiction detection, replay and the interface read the columns in clear and per-row decryption would touch every read path; recorded as future work and recommended for backups.
- Disabling the trigger and deleting rows: breaks provenance links and replay, and hides that anything happened. Rejected.
- Leaving content in place with access controls: does not satisfy erasure. Rejected.
