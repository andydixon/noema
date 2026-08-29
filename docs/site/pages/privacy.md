History in Noema is append-only because provenance, replay and forensic reconstruction depend on it. People's words nevertheless sometimes have to be removed. This page explains what redaction is, how it keeps the shape of history while removing its content, what it can and cannot guarantee, and why it is the only write the append-only store admits.

## Retained, redacted, irrecoverable

Everything is **retained** by default: the row, its content, identifiers, links and timestamps. A **redacted** row keeps its identifiers, type, timestamps, links, scores and breakdowns but its content columns hold a fixed marker, and a ledger records when, why, by whom, under which policy and a hash of what was removed. **Irrecoverable** means no copy remains anywhere the system controls: live tables, stored snapshot archives and future exports. Rows in cognitive history are never **deleted**; a redacted row is the strongest erasure the system performs, and it is deliberately visible as one.

## How it differs

Most systems offer deletion, which destroys provenance along with content, or nothing at all. Noema separates the two. After a redaction an operator can still see that a belief existed, how strong it was, which families supported it, what later depended on it and which replay steps touched it; what they cannot see is the words. The ledger's content hash lets a later holder of the original prove what was removed without the system retaining it. The redaction itself is an event in the mind's history, so the record that something was removed survives even a cascade.

## Guarantees and their edge

Redaction is administrator-only, protected against cross-site request forgery, audited, and performed by a single database function that runs under a transaction-local flag the application never sets; it can update content columns and nothing else, and cannot delete. Stored snapshots are scrubbed and re-signed. Replays skip a redacted stimulus and label the step rather than invent content. The guarantee ends at the system's own storage: backups, exports and replicas taken before a redaction still hold the content, as does anything already sent to a model provider. Cryptographic erasure was considered and not adopted because retrieval, search, contradiction detection, the interface and replay all read these columns in clear; it remains the right tool for backups, which Noema does not manage.

{{include docs/privacy.md}}
