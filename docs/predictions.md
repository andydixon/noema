# Prediction system

A prediction is an explicit expectation with a resolution window and criteria that code can check (spec §11). Predictions are how the mind finds out it was wrong.

## Structure

| Field | Meaning |
|---|---|
| statement | what is expected, in words |
| confidence | 0.05–0.95; certainty is refused |
| window_start / window_end | when the expectation applies |
| criteria | `event_match` (terms occur or stay absent in events of given types), `belief` (a belief reaches a confidence), or `operator` (a person judges) |
| belief_id | the belief the prediction was derived from, if any |
| status | open → confirmed, refuted, expired or withdrawn |

`prediction_results` is append-only and keeps every outcome with the stated confidence and the error, so calibration cannot be rewritten.

## Resolution

A scheduler runs the resolver every minute. For each open prediction it judges deterministically (`predictions.Judge`): event-match predictions look at the mind's events inside the window (excluding the prediction's own creation and other prediction events); belief predictions compare the current confidence; operator predictions expire a week after the window. Operators can also confirm, refute or withdraw from the UI, API or CLI.

Outcomes emit `prediction_resolved` or, for refutations, `prediction_error` with salience scaled by how confident the miss was. The result becomes evidence on the linked belief: confirmed adds support (weight 0.3), refuted adds opposition (weight 0.5), and the belief is re-evaluated with the usual arithmetic. The motivation rules raise arousal and uncertainty and lower confidence after a miss; curiosity turns the miss into a question that competes for attention and, if admitted, becomes an investigation goal.

## Where predictions come from

- Operators, through the Predictions page, the API or `noemactl`.
- Causal hypotheses: when the associate process turns "A may have caused B" into a belief, a prediction is derived automatically — the effect should recur within 24 hours if the cause is real — with the belief's confidence, so a miss lowers the hypothesis.
- Reflection reads calibration over the last ninety days and raises an overconfidence warning through metacognition; it does not create predictions. Consolidation creates none either. Prediction outcomes also feed source reliability (docs/beliefs.md).

## Calibration

The dashboard shows accuracy (confirmed over decided), the Brier score, and buckets of stated confidence against actual hit rate over the last 90 days. A mind whose mean stated confidence exceeds its hit rate by more than 0.15 across at least five decided predictions is flagged as overconfident; metacognition (phase 10) uses the same signal to discount new confident claims.
