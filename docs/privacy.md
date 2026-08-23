# Privacy, retention and redaction

Noema's history is append-only because provenance, replay and forensic reconstruction depend on it. Personal or sensitive content can nevertheless need to be removed. This page defines exactly what the system can and cannot do about that.

## Definitions

| Term | Meaning in Noema |
|---|---|
| **retained** | the row exists with its content, identifiers, links and timestamps. The default for everything. |
| **redacted** | the row exists with its identifiers, type, timestamps and links intact, but its content columns hold the fixed marker `[redacted]`. A ledger row records when, why, by whom, under which policy, and a SHA-256 of the removed content. |
| **irrecoverable** | no copy of the content remains anywhere this system controls: the live tables, stored snapshot archives and future exports. Backups, exports and replicas made *before* the redaction are outside the system and are the operator's responsibility (see below). |
| **deleted** | not offered for cognitive history. Rows in append-only tables are never deleted; a redacted row is the strongest erasure the system performs. Ordinary mutable rows (sessions, tokens, approvals) are deleted by their own lifecycles. |

## The redaction path

`POST /api/v1/minds/{mind}/redactions` (administrators; also the mind's **Redactions** page) takes a kind (`event`, `memory`, `message`, `llm_call`, `belief`, `assertion`), an id, a reason, an optional policy name and a cascade flag. It runs in one transaction:

1. The row's content is hashed and a `redactions` ledger row is written (append-only, unique per target).
2. `noema_redact()` replaces the content columns. It is the only code path that may update an append-only table: it sets a transaction-local flag that the append-only trigger checks, touches nothing but content and the redaction bookkeeping, and cannot delete. The function is `SECURITY DEFINER`, revoked from `PUBLIC`, and the application never sets the flag itself.
3. A `redaction` event is appended to the mind's history naming the target, the policy and the hash, never the content.
4. With `cascade`, rows derived from the target are redacted too and linked to the primary ledger row: for an event, the episodes encoded from it, the message it carried and its reply, the model calls that worded them, the assertions made in it and the beliefs whose evidence roots at it; for each redacted row, the events that quote it (a perception, an encoding, a belief creation). Redaction events are never cascaded, so the record of redaction survives.
5. Stored snapshot archives of the mind are scrubbed of the same rows, the archive records the redacted ids, and its checksum is recomputed; archives still validate.
6. The action is written to the audit log.

What each kind replaces: events lose `summary`, `payload` and free-text metadata (structural keys such as conversation, message, cycle and belief ids are kept); memories lose `content` and `detail`; messages lose `content` and `intent`; model calls lose `request` and `response`; beliefs lose `proposition` and `detail` and their versions, evidence notes and alternatives; assertions lose `proposition`, `context` and notes. Scores, statuses, confidence breakdown numbers, families, links and timestamps are untouched, so the epistemic shape of the history remains inspectable: an operator can still see that a belief existed, how strong it was, what supported it and what later depended on it.

## Why not cryptographic erasure

Envelope-encrypting payloads with a per-row or per-subject key and destroying the key on erasure was considered and not adopted in this release. Full-text search, semantic retrieval, contradiction detection, the interface and replay all read these columns in clear; per-row decryption on every read would touch every code path and index, and search over ciphertext would need either plaintext shadow columns (defeating the purpose) or a redesign of retrieval. The marker-and-hash approach achieves the same end state for the live database and stored archives with a small, auditable surface. Cryptographic erasure remains the right answer for **backups**, which this system does not manage: encrypt backups under keys you can retire, and treat a redaction as a signal to rotate them on your retention schedule.

## Threat model

| Scenario | Outcome |
|---|---|
| Database compromise after redaction | the content is gone; the hash reveals nothing about it. |
| Database compromise before redaction, or a backup or replica taken before it | the content is present. Redaction cannot reach copies the system did not make. |
| Administrator misuse | redaction is administrator-only, CSRF-protected, audited, and leaves an append-only ledger and a history event; it cannot be used to hide that something happened, only what its content was. |
| Snapshots and exports | stored snapshots are scrubbed; exports and archives made after the redaction carry the marker; those made before do not. |
| Replay | a redacted stimulus is not replayed and the step is labelled `redacted`; the replay never fabricates content. |
| Logs | Noema does not log message, memory or payload content; the structured logger redacts secret-shaped values. Web-server access logs in front of Noema are outside its control. |
| Model providers | content already sent to a provider before the redaction is subject to that provider's retention, not Noema's. |

## Retention

There is no automatic retention purge. Retention is an operator policy applied through the redaction path with a policy name, so every removal is attributable. A future bulk policy runner would use the same function and ledger.
