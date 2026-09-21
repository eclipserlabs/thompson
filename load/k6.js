import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '30s', target: 20 },
    { duration: '1m', target: 50 },
    { duration: '30s', target: 0 },
  ],
  thresholds: {
    'http_req_duration{scenario:health}': ['p(95)<100'],
    'http_req_duration{scenario:snapshots}': ['p(95)<200'],
    // Router selection path is separate from the control-plane read path;
    // keep its own budget so a slow provider cannot hide behind snapshot SLOs.
    'http_req_duration{scenario:router}': ['p(95)<1500'],
    http_req_failed: ['rate<0.01'],
  },
};

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const TOKEN = __ENV.CONTROL_PLANE_TOKEN || '';
// Go gateway under test, if different from the control-plane. Unset disables
// the router section so control-plane-only runs keep working.
const ROUTER_BASE = __ENV.ROUTER_BASE_URL || '';

function headers() {
  const h = { 'Content-Type': 'application/json' };
  if (TOKEN) h['Authorization'] = `Bearer ${TOKEN}`;
  return h;
}

export default function () {
  let res = http.get(`${BASE}/health`, { tags: { scenario: 'health' } });
  check(res, { 'health 200': (r) => r.status === 200 });

  res = http.get(`${BASE}/metrics`, { tags: { scenario: 'metrics' } });
  check(res, { 'metrics 200': (r) => r.status === 200 && r.body.includes('traverse_snapshots_total_pulls') });

  res = http.get(`${BASE}/snapshots`, { headers: headers(), tags: { scenario: 'snapshots' } });
  check(res, { 'snapshots 200/401': (r) => r.status === 200 || r.status === 401 });

  if (ROUTER_BASE) {
    const payload = JSON.stringify({ model: 'router-smoke', messages: [{ role: 'user', content: 'k6 smoke' }] });
    const rRes = http.post(`${ROUTER_BASE}/v1/chat/completions`, payload, {
      headers: { 'Content-Type': 'application/json' },
      tags: { scenario: 'router' },
    });
    check(rRes, { 'router 2xx/4xx/5xx (served)': (r) => r.status >= 200 && r.status < 600 });
  }

  sleep(1);
}
