# Performance pass

`tests/perf` is an opt-in load test. It seeds a fresh schema with a realistic
volume for one mind plus a second "noise" mind (so scoping matters), times
the hot read paths in steady state, and rejects any plan that sequentially
scans the three large tables.

```
NOEMA_PERF=1 NOEMA_TEST_DATABASE_URL=postgres://... go test -count=1 -v -run TestHotPaths ./tests/perf/
```

Seed: 60 000 events, 6 000 memories, 1 500 beliefs with evidence. Ceiling per
read: 400 ms (a regression guard, not a target).

## Results (PostgreSQL 16 in Docker, laptop, 2026-09-14)

| Read | Before 0016 | After 0016 |
|---|---|---|
| events recent 50 | 3.2 ms | 1.2 ms |
| events by type | 1.8 ms | 1.1 ms |
| events after seq (runtime cursor) | 2.2 ms | 1.2 ms |
| events summary search | 3.3 ms | 1.4 ms |
| events stats (group by category) | 13.8 ms | 15.8 ms |
| memory list 50 | 4.1 ms (seq scan) | 0.5 ms |
| memory text search | 1.7 ms | 0.5 ms |
| memory fused retrieve | 31 ms | 27 ms |
| beliefs list | 3.6 ms (seq scan) | 3.6 ms |
| beliefs search / contested / uncertain / stats / recent changes | ≤ 3 ms | ≤ 3 ms |
| snapshot capture (all state tables) | 288 ms | 296 ms |

Migration `0016_hot_path_indexes` adds the indexes the planner needed for the
two list orders that had fallen back to sequential scans, plus
`(mind_id, category, seq)` on events for replay's stimulus scan and
`(mind_id, created_at)` on belief versions for the recent-changes view.

## What was deliberately left alone

- **Fused retrieval (~27 ms)** is the most expensive per-cycle read; it runs a
  ranked full-text query capped at 50 candidates plus optional vector search.
  It is well inside the cycle budget; tune only if cycle latency becomes visible.
- **Snapshot capture (~300 ms)** serialises every state table; it is an
  operator action, not a cycle path.
- **Event stats (~16 ms)** is a group-by over all of a mind's events, used on
  the overview page. A materialised counter would be premature at this volume.

## Rerun before touching queries

The plan checks fail loudly when a future query drifts off its index, which is
the cheapest way to catch a regression that would otherwise only show up with
months of history.
