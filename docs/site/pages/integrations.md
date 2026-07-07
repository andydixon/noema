Integrations bring the outside world into a mind as observations. Each source is treated as hostile data: bounded, sniffed, fingerprinted, tagged with provenance and trust, and never executed. This page covers the four built-in kinds, the shared machinery, the secrets model and the capabilities integrations expose to minds.

## Kinds

| Kind | What it watches | Trust | Secret |
|---|---|---|---|
| research folder | a directory of documents | low (forced) | none |
| git | a repository, mirrored bare and shallow | source | optional token |
| Prometheus / VictoriaMetrics | PromQL queries with thresholds or change detection | source | optional bearer token |
| webhook | signed HTTP deliveries | source | required HMAC secret |

**Research folder.** The poller opens the directory with a root-confined file API, so symlinks and traversal cannot escape it. Files are type-sniffed rather than trusted by extension; archives, executables, disk images and anything binary are rejected and recorded as rejected; text-like documents are extracted in-process with size caps, fingerprinted by content, and ingested once. HTML is reduced to text. Each document becomes an observation with an excerpt and a reference to the file and fingerprint, and the document ledger shows ingested, duplicate, rejected and failed items.

**Git.** Repositories are mirrored with hooks disabled and the external transport protocol refused; git runs with argument vectors only, never a shell. New commits become observations (author, date, subject, files) and co-change statistics are kept as integration state. A mind with a git integration gains the `repository.read` capability, which reads one file at one ref through the same guarded process and refuses paths that would leave the tree.

**Prometheus.** Queries are named expressions with optional thresholds. The poller evaluates them, records the last value, and emits observations when a threshold is crossed or the value changes by a large relative amount, with salience proportional to the size of the change. A mind gains `read_metrics` for ad-hoc queries against the same endpoint.

**Webhooks.** A delivery must carry a timestamp header and an HMAC-SHA256 signature over `timestamp.body` with the integration's secret. Deliveries are refused when unsigned, older than five minutes, replayed (signature already seen), larger than 256 KiB, or over the per-address rate limit. A webhook without a configured secret refuses everything. Accepted deliveries become observations with the sender's title, text, reference and salience.

## Shared machinery

Integrations have an interval (minimum thirty seconds), a health status (new, ok, error, stale, disabled), the last run and error, counts of items and observations, and a run history. The scheduler ticks every minute and runs those that are due, in isolation, with a timeout, recording each run. Every observation carries the integration's id, name and kind, the trust level and a reference back to the source item, and the provenance is visible on the event page.

## Secrets

Configuration never holds secrets: keys named like token, secret, password or api_key are rejected at validation. Secrets are stored as references (`env:`, `file:` or `enc:` under the master key) and are never rendered back into forms or API responses.

{{include docs/integrations.md}}
