// baseline load for POST /check. a 429 here is the limiter working,
// not an error — only unexpected statuses count as failures.
//
//   k6 run loadtest/check.js
//   k6 run -e BASE_URL=http://localhost:8080 -e RATE=300 -e DURATION=30s loadtest/check.js

import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const RATE = Number(__ENV.RATE || 300);
const DURATION = __ENV.DURATION || '30s';
const VUS = Number(__ENV.VUS || 50);
const MAX_VUS = Number(__ENV.MAX_VUS || 200);

export const options = {
  scenarios: {
    checks: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: VUS,
      maxVUs: MAX_VUS,
    },
  },
};

http.setResponseCallback(http.expectedStatuses(200, 429));

const allowed = new Counter('rl_allowed');
const denied = new Counter('rl_denied');

const tiers = ['free', 'paid'];
const endpoints = ['/download', '/upload'];

export default function () {
  // ~100 identities across two tiers and two endpoints: some identities
  // stay under their limit, hot ones blow through it — both code paths
  // show up in the latency numbers
  const identity = `user-${Math.floor(Math.random() * 100)}`;
  const tier = tiers[Math.random() < 0.7 ? 0 : 1];
  const endpoint = endpoints[Math.random() < 0.8 ? 0 : 1];

  const res = http.post(
    `${BASE_URL}/check`,
    JSON.stringify({ identity, tier, endpoint }),
    { headers: { 'Content-Type': 'application/json' } },
  );

  check(res, { 'decision returned': (r) => r.status === 200 || r.status === 429 });
  if (res.status === 200) allowed.add(1);
  else if (res.status === 429) denied.add(1);
}
