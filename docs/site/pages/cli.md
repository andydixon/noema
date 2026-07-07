`noemactl` is a thin client for the HTTP API with a few offline commands that talk to the database directly. It prints tables by default and JSON with `-json` where a command supports it, exits non-zero on API errors with the error message, and never stores credentials.

## Environment

| Variable | Purpose |
|---|---|
| `NOEMA_URL` | base URL of the daemon, default `http://127.0.0.1:8080` |
| `NOEMA_API_TOKEN` | bearer token from the account page; required for API commands |
| `NOEMA_DATABASE_URL` | for the offline commands only (`migrate`, `user`) |

## Commands

```
status     show daemon status
version    print version
migrate    up|down|status – manage database schema (offline)
user       create <username> [-role r] – manage users (offline)

mind       list|create|inspect|clone|personality – manage minds
template   list – personality templates
ask        <mind> <text...> [-wait 30] – talk to a mind and wait for its reply
observe    <mind> <text> – inject an operator observation
event      inspect <id>|list <mind> [-type t] [-n 50]|tail <mind> – cognitive events
workspace  show <mind>|cycle <mind>|pause <mind>|resume <mind>|reset <mind> – workspace and cognition control
narrative  <mind> – current internal narrative
state      show <mind>|reset <mind> – drives and affect

memory     search <mind> <query>|list <mind>|inspect <mind> <id>|add <mind> [-kind k] [-epistemic e] <text>
belief     list --mind m [-status s]|inspect <mind> <id>|assert <mind> <text>|wrong <mind> <id> <reason>
goal       list <mind>|add <mind> <title...>
prediction list <mind>|calibration <mind>
entity     list <mind> [query]|inspect <mind> <id>|merge <mind> <id> <other>|distinct <mind> <id> <other>

consolidate <mind> – run consolidation now
reflect    <mind> – run reflection now
dreams     <mind> list|run|promote <id> [note]|reject <id> [reason]
notifications list [-mind m]|read-all

approvals  list|approve <id> [note]|session <id>|deny <id> [note]
policy     list|add -mind m -capability c -target t -effect e [-reason r]|delete <id>
invoke     <mind> <capability> [target] [json-params] – invoke a capability through policy

provider   list|test <provider-id> [model]
prompt     list|show <name>
integrations list|add <mind> <kind> <name> <target> [secret]|run <id>|runs <id>|delete <id>

snapshots  <mind> list|create [name]|fork <snapshot-id> <slug>|export|import <file> <slug>
compare    <mind-a> <mind-b>
replay     <mind> list|start <snapshot-id> [from] [to]|show <replay-id>
experiments list|create <file.json>|show <id>|run <id>|export <id>
```

## Examples

Talk to a mind and wait up to a minute for the reply, with the evidence it rests on:

```
noemactl ask dani -wait 60 "Is atlas the primary database host?"
```

Watch cognition live:

```
noemactl event tail dani
```

Inject an observation as an operator and see what the mind does with it:

```
noemactl observe dani "server-03 reported SMART CRC errors at 09:12"
noemactl workspace show dani
noemactl belief list --mind dani -status contested
```

Correct a belief and stop it being re-derived:

```
noemactl belief wrong dani 01a0… "atlas was renamed; db-primary is the host now"
```

Let a mind read metrics, ask before it fetches URLs, and see the decisions:

```
noemactl policy add -mind dani -capability read_metrics -target '*' -effect allow -reason "read-only telemetry"
noemactl policy add -mind dani -capability http.fetch -target 'https://status.example.com/*' -effect ask
noemactl approvals list
noemactl approvals approve 01a0… "one-off status check"
```

Connect sources:

```
noemactl integrations add dani research_folder papers ./research
noemactl integrations add dani prometheus prod http://prom.internal:9090 env:PROM_TOKEN
noemactl integrations add dani webhook alerts - env:ALERT_HOOK_SECRET
noemactl integrations run 01a0…
```

Snapshot, fork, compare, replay:

```
noemactl snapshots dani create "before the migration incident"
noemactl snapshots dani fork 01a0… dani-b
noemactl compare dani dani-b
noemactl replay dani start 01a0…
noemactl replay dani show 01a0…
```

Export a mind to a file and import it elsewhere:

```
noemactl snapshots dani export > dani.noema.json
NOEMA_URL=https://other.example noemactl snapshots x import dani.noema.json dani
```

Run an experiment from a definition file (see [snapshots, forks, replay and experiments](snapshots-replay) for the format):

```
noemactl experiments create capacity3.json
noemactl experiments run 01a0…
noemactl experiments export 01a0… > results.json
```
