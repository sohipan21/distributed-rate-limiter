# What actually limits throughput

The sweep in `../curve.md` tracks the offered rate to 14k and falls apart by
16k. Four experiments to find out what stopped it. Same machine, same Redis,
generator co-located throughout, so the comparisons hold even though the
absolute numbers are laptop-bound.

## 1. Is it the generator?

Partly, and it took two mistakes to pin down.

`preAllocatedVUs` and `maxVUs` both bias the result, in opposite directions. Too
few and a 20s run spends itself ramping and drops iterations; too many and a
thousand idle JS runtimes take CPU the service needed. Measured at a fixed
12,000 offered, varying only `preAllocatedVUs`:

| preallocated VUs | achieved | p50 | p99 | VUs actually used |
|-----------------:|---------:|----:|----:|------------------:|
| 100 | 11,805 | 4.29ms | 66.82ms | 384 |
| 400 | 11,913 | 4.53ms | 50.09ms | 263 |
| 1500 | 11,664 | 18.56ms | 285.29ms | 2,284 |

Same service, same offered rate, 5.7x difference in p99 from a generator knob.
An earlier sweep with 50 preallocated reported a ceiling of 11.5k at 15k
offered; with 1000 preallocated the same rate delivered 14.9k. `check.js` now
defaults to `rate/30` preallocated with a generous `maxVUs`, since maxVUs is
grown into on demand and costs nothing until used.

Rows where a run exhausted the VU pool are flagged in `../curve.md` and
excluded from the knee calculation — they measure k6, not the service.

## 2. Is it Redis?

`INFO commandstats`, reset before every run — the `evalsha_usec_per_call`
column in [`../runs.csv`](../runs.csv):

| offered | evalsha calls | usec_per_call |
|--------:|--------------:|--------------:|
| 8,000 | 159,870 | 25.96 |
| 12,000 | 238,235 | 25.26 |
| 14,000 | 276,846 | 23.38 |
| 16,000 | 285,520 | 28.74 |

Medians of the three runs at each rate, same as everywhere else here.

Script execution is flat at ~25µs from 8k all the way through the cliff. At 14k
rps that is ~0.33s of script execution per second of wall clock — a third of one
core. Redis sat at 62% CPU while the sweep was falling over. It is not the
constraint.

## 3. Is it nginx? — yes

One node hit directly on :8081, versus three nodes behind nginx on :8080:

| offered | 1 node direct | 3 nodes via nginx |
|--------:|--------------:|------------------:|
| 12,000 | 11,933 @ p99 6.6ms | 11,889 @ p99 61ms |
| 15,000 | 14,820 @ p99 32.4ms | — |
| 20,000 | 15,077 @ p99 773ms | — |

A single node sustains ~15k. Three nodes behind the proxy sustain ~13.8k and are
roughly 9x worse at p99 for the same offered rate. The proxy is not adding
capacity, it is subtracting it — and it is the largest CPU consumer at
saturation (160% vs ~79% per node and 63% for Redis — the `*_cpu_pct`
columns in [`../runs.csv`](../runs.csv)).

## 4. How much of that was proxy config

The original `nginx.conf` used the default `worker_connections 512` and had no
upstream keepalive, so every request opened a fresh connection to a node.
Measured at 12k offered, both configs, everything else identical:

| nginx config | achieved | p50 | p99 | dropped |
|---|--------:|----:|----:|--------:|
| original | 6,719 | 69.1ms | 2,302ms | 63,587 |
| `worker_connections 8192` + `keepalive 256` | 11,875 | 5.3ms | 52.3ms | 3,675 |

Nearly 2x the throughput and 44x better p99 from proxy config alone. A sweep run
against the original config would have reported a ~6.7k "ceiling" belonging
entirely to nginx.

## Conclusion

The rate limiter is not the bottleneck at any rate measured. One node serves
~15k rps against Redis at p99 32ms; Redis has roughly 3x headroom on script
execution. The clustered ceiling of ~13.8k is imposed by the single co-located
nginx, and the collapse past 16k is the whole box — service plus generator —
running out of cores.

The fix is more proxy, not more limiter: several nginx instances, a proxy that
scales better across cores, or clients sharding across nodes directly.
Untested here because the box has no spare cores to test it with. A second
machine for the generator is the honest next step, and would also remove the
measurement sensitivity in experiment 1.
