Predictions give a mind a way to be wrong in a measurable way. A prediction is an explicit expectation with criteria a resolver can check; its outcome feeds back into the beliefs it came from and into a calibration record that says how well confidence has tracked reality.

## Making predictions

Predictions are created by the executive when a belief with sufficient confidence implies something checkable about the future, by operators through the interface or `noemactl prediction`, and by experiments as expectations. Each has a statement, a subject, a confidence, a time window, machine-checkable criteria (an event of a type whose summary contains a term, a belief crossing a confidence, a metric crossing a threshold), the belief it derives from and its origin event.

## Resolution

A scheduled resolver evaluates open predictions once a minute. Criteria met inside the window resolve to **confirmed**; a window that closes without the criteria resolves to **expired** (treated as refuted for calibration unless the criteria say otherwise); explicit contrary evidence resolves to **refuted**. Operators can resolve manually with a reason. Every resolution is a `prediction_resolved` event; a wrong one is also a `prediction_error` event with the size of the error.

## Feedback

A prediction error adds evidence against the belief the prediction derived from, weighted by the size of the error, and lowers confidence through the ordinary belief arithmetic. A confirmed prediction adds support. Errors also move affect (uncertainty up, confidence down) and drives (uncertainty reduction up), and give curiosity a question. The demonstration scenario in the plan, "prediction error lowers confidence and produces a curiosity goal", is exactly this chain and is covered by tests.

## Calibration

The calibration page bins resolved predictions by stated confidence and shows the observed frequency in each bin, the Brier score and the counts. A well-calibrated mind's 70 % predictions come true about 70 % of the time. The page exists so that an operator can judge whether a mind's confidence means anything, and so that experiments that change attention or personality can be compared on it.

{{include docs/predictions.md}}
