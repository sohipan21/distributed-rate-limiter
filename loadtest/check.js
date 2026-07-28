// load for POST /check. a 429 here is the limiter working, not an error —
// only unexpected statuses count as failures.
//
//   k6 run loadtest/check.js                                    # 300 rps, 30s
//   k6 run -e RATE=5000 -e DURATION=60s loadtest/check.js       # one fixed step
//   k6 run -e SCENARIO=ramp loadtest/check.js                   # continuous ramp
//
// scripts/saturate.sh drives the fixed-step form across a rate sweep to find
// the knee; the ramp form is for watching the shape in one run.

import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const RATE = Number(__ENV.RATE || 300);
const DURATION = __ENV.DURATION || '30s';
// Both of these bite, in opposite directions, and when the generator shares a
// machine with the service they change the answer more than the service does.
// Measured at a fixed 12k offered, varying only preAllocatedVUs:
//     100 -> p99  66.8ms      400 -> p99  50.1ms      1500 -> p99 285.3ms
// Too few and a short run spends itself ramping and drops iterations; too many
// and a thousand idle JS runtimes take CPU the service needed. maxVUs is the
// cheap one — k6 grows into it on demand — so keep it generous and keep the
// preallocation small.
const VUS = Number(__ENV.VUS || Math.max(50, Math.ceil(RATE / 30)));
const MAX_VUS = Number(__ENV.MAX_VUS || Math.max(4000, RATE * 2));
const KEYS = Number(__ENV.KEYS || 100);
const API_KEY = __ENV.API_KEY || '';
const OUT_JSON = __ENV.OUT_JSON || '';

const step = {
  executor: 'constant-arrival-rate',
  rate: RATE,
  timeUnit: '1s',
  duration: DURATION,
  preAllocatedVUs: VUS,
  maxVUs: MAX_VUS,
};

// one continuous climb, for the shape rather than per-step numbers
const ramp = {
  executor: 'ramping-arrival-rate',
  startRate: 1000,
  timeUnit: '1s',
  preAllocatedVUs: 500,
  maxVUs: MAX_VUS,
  stages: [
    { target: 2000, duration: '30s' },
    { target: 5000, duration: '30s' },
    { target: 10000, duration: '30s' },
    { target: 20000, duration: '30s' },
    { target: 40000, duration: '30s' },
  ],
};

export const options = {
  scenarios: { checks: __ENV.SCENARIO === 'ramp' ? ramp : step },
  // k6 computes avg/min/med/max/p(90)/p(95) by default — p(99) has to be asked
  // for, and it silently reports 0 if you don't
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'p(99.9)', 'max'],
  // these define the knee objectively instead of by eyeball. no abortOnFail:
  // a breached step should still finish so the whole curve gets recorded.
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(99)<50'],
    // the one that proves the generator wasn't the limit. per-second, not an
    // absolute count: every arrival-rate run drops a few hundred while the VU
    // pool spins up, and an absolute budget would just flag longer runs.
    dropped_iterations: ['rate<100'],
  },
};

http.setResponseCallback(http.expectedStatuses(200, 429));

const allowed = new Counter('rl_allowed');
const denied = new Counter('rl_denied');

const tiers = ['free', 'paid'];
const endpoints = ['/download', '/upload'];

export default function () {
  // identities across two tiers and two endpoints: some stay under their
  // limit, hot ones blow through it — both code paths show up in the latency
  const identity = `user-${Math.floor(Math.random() * KEYS)}`;
  const tier = tiers[Math.random() < 0.7 ? 0 : 1];
  const endpoint = endpoints[Math.random() < 0.8 ? 0 : 1];

  const headers = { 'Content-Type': 'application/json' };
  // auth is on iff the server was given a config with api keys; when it is,
  // identity and tier come from the key and the body fields are ignored
  if (API_KEY) headers['X-API-Key'] = API_KEY;

  const res = http.post(
    `${BASE_URL}/check`,
    JSON.stringify({ identity, tier, endpoint }),
    { headers },
  );

  check(res, { 'decision returned': (r) => r.status === 200 || r.status === 429 });
  if (res.status === 200) allowed.add(1);
  else if (res.status === 429) denied.add(1);
}

// emit one machine-readable row per run so saturate.sh can build the curve
// without parsing the human summary
export function handleSummary(data) {
  const out = { stdout: '' };
  if (!OUT_JSON) return out;

  const m = data.metrics;
  const pick = (name, field) => (m[name] && m[name].values[field]) || 0;

  out[OUT_JSON] = JSON.stringify(
    {
      offered_rps: RATE,
      duration: DURATION,
      achieved_rps: pick('http_reqs', 'rate'),
      requests: pick('http_reqs', 'count'),
      error_rate: pick('http_req_failed', 'rate'),
      dropped_iterations: pick('dropped_iterations', 'count'),
      p50_ms: pick('http_req_duration', 'med'),
      p90_ms: pick('http_req_duration', 'p(90)'),
      p95_ms: pick('http_req_duration', 'p(95)'),
      p99_ms: pick('http_req_duration', 'p(99)'),
      p999_ms: pick('http_req_duration', 'p(99.9)'),
      max_ms: pick('http_req_duration', 'max'),
      allowed: pick('rl_allowed', 'count'),
      denied: pick('rl_denied', 'count'),
      vus_max: pick('vus', 'max'),
      // vus_max against this is what separates "the service was slow" from
      // "the generator ran out of VUs and the row is meaningless"
      max_vus_configured: MAX_VUS,
    },
    null,
    2,
  );
  return out;
}
