# Where the latency goes

Offered 2000 rps, achieved 1997, 20s. Means over the run.

| layer | mean ms | share | source |
|---|--------:|------:|---|
| lua inside redis | 0.021 | 2% | measured (commandstats) |
| redis round trip + pool wait | 0.131 | 16% | derived (redis call − lua) |
| handler + policy | 0.001 | 0% | derived (decision − redis call) |
| nginx + go http + wire | 0.689 | 82% | **residual** (k6 − decision) |
| total | 0.842 | | = k6 mean by construction |

Directly measured, independently of each other:
- whole decision, in-process: 0.1532 ms
- redis call, client side: 0.1519 ms
- lua execution, server side: 0.0208 ms
- end to end, at the client: 0.8424617675768997 ms

The last row is a residual, not a measurement — it absorbs everything
between k6 and the handler, including any error in the other three. The
total matching the k6 mean is arithmetic, not corroboration. What does
corroborate: the lua figure here should match the evalsha_usec_per_call
column in loadtest/results/saturation/runs.csv, and the decision figure
should exceed the redis figure by the handler's own work.
