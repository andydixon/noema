# Integrations

Integrations bring external data into a mind as **observations**. Every item is
stored with its source, trust level and reference, and is treated as data: an
instruction found inside an ingested file, commit message, metric label or
webhook body is never followed. Integrations never execute anything.

| Kind | What it does | Trust | Secret |
|---|---|---|---|
| `research_folder` | Watches a directory. New or changed text files (md, txt, csv, json, html, source) become observations with a content fingerprint. Archives, executables, symlinks and binaries are rejected and recorded as such. | `low` (forced) | none |
| `git` | Mirrors a repository bare and shallow (`--mirror` fetch, hooks disabled, `protocol.ext` refused). New commits become observations; co-change pairs are kept as state. | `source` | optional token via `SecretRef` |
| `prometheus` | Evaluates PromQL queries against Prometheus or VictoriaMetrics. Threshold crossings and large relative changes become observations. | `source` | optional bearer token |
| `webhook` | Receives signed POSTs at `/api/v1/webhooks/{id}`. | `source` | **required** HMAC secret |

## Provenance

Each observation carries `metadata.integration`, `metadata.integration_id`,
`metadata.kind`, `metadata.trust` and a `reference` (file path + fingerprint,
commit hash, metric expression or webhook reference). Documents are listed per
integration with status `ingested`, `duplicate`, `rejected` or `failed`.

## Webhooks

```
X-Noema-Timestamp: <unix seconds>
X-Noema-Signature: sha256=HMAC_SHA256(secret, timestamp + "." + raw_body)
{"title":"…","text":"…","reference":"…","salience":0.6}
```

Deliveries are refused when the timestamp is more than 300 s from now, the
signature does not match, the signature has been seen before (replay), the body
exceeds 256 KiB, or the per-IP rate limit is exceeded. A webhook without a
configured secret refuses everything.

## Secrets and configuration

Configuration never holds secrets: keys named like `token`, `secret`, `password`
or `api_key` are rejected. Secrets are stored as references (`env:NAME`,
`file:/path`, or `enc:` under `NOEMA_MASTER_KEY`) and are never rendered back.

## Capabilities exposed to minds

- `read_metrics` (default ALLOW, low risk): target = Prometheus integration name, param `expr`.
- `repository.read` (default ASK, medium risk): target = git integration name, params `path`, `ref`.

Both resolve only integrations connected to the invoking mind, so policy and the
operator decide which endpoints a mind can reach.

## Operating

- UI: **Integrations** lists health, last run, error and counts; the detail page shows configuration, recent runs and ingested documents.
- CLI: `noemactl integrations list|add|run|runs|delete`.
- The scheduler ticks every minute and runs integrations whose interval has elapsed. Failures mark the integration `error` and are shown in the UI; sources that fail repeatedly become `stale`.
