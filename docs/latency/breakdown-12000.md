# Where the latency goes

Offered 12000 rps, achieved 11860, 20s. Means over the run.

| layer | mean ms | share | source |
|---|--------:|------:|---|
| lua inside redis | 0.025 | 0% | measured (commandstats) |
| redis round trip + pool wait | 4.211 | 40% | derived (redis call − lua) |
| handler + policy | 0.002 | 0% | derived (decision − redis call) |
| nginx + go http + wire | 6.357 | 60% | **residual** (k6 − decision) |
| total | 10.595 | | = k6 mean by construction |

Directly measured, independently of each other:
- whole decision, in-process: 4.2384 ms
- redis call, client side: 4.2363 ms
- lua execution, server side: 0.0253 ms
- end to end, at the client: 10.5954 ms

The last row is a residual, not a measurement — it absorbs everything
between k6 and the handler, including any error in the other three. The
total matching the k6 mean is arithmetic, not corroboration. What does
corroborate: the lua figure here should match the evalsha_usec_per_call
column in loadtest/results/saturation/runs.csv, and the decision figure
should exceed the redis figure by the handler's own work.
