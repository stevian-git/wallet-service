import http from 'k6/http';
import { check, sleep } from 'k6';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const API_KEY = __ENV.API_KEY || 'dev_api_key';
const HOT = __ENV.HOT === '1';
const VUS = parseInt(__ENV.VUS || '10');
const DURATION = __ENV.DURATION || '30s';
const NUM_WALLETS = 20;
const HOT_WALLET_INDEX = 0;

const headers = {
  'Content-Type': 'application/json',
  'X-API-Key': API_KEY,
};

const accountIDs = [];

export const options = {
  vus: VUS,
  duration: DURATION,
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(50)', 'p(95)', 'p(99)'],
};

export function setup() {
  console.log(`Creating ${NUM_WALLETS} wallets with topups...`);

  for (let i = 0; i < NUM_WALLETS; i++) {
    const ownerRef = `load-wallet-${i}`;
    const createRes = http.post(
      `${BASE_URL}/v1/accounts`,
      JSON.stringify({
        owner_ref: ownerRef,
        currency: 'IDR',
      }),
      {
        headers: {
          ...headers,
          'Idempotency-Key': `setup-account-${ownerRef}`,
        },
      }
    );

    check(createRes, {
      [`account ${i} created`]: (r) => r.status === 201 || r.status === 422,
    });

    if (createRes.status === 201) {
      const account = JSON.parse(createRes.body);
      accountIDs.push(account.id);

      const topupRes = http.post(
        `${BASE_URL}/v1/topups`,
        JSON.stringify({
          destination_account_id: account.id,
          amount: 1000000,
          currency: 'IDR',
        }),
        {
          headers: {
            ...headers,
            'Idempotency-Key': `setup-topup-${ownerRef}`,
          },
        }
      );

      check(topupRes, {
        [`topup ${i} succeeded`]: (r) => r.status === 201 || r.status === 422,
      });
    }
  }

  console.log(`Created ${accountIDs.length} wallets`);
  if (HOT) {
    console.log(`Hot wallet mode enabled, target: wallet ${HOT_WALLET_INDEX}`);
  }

  return { accountIDs };
}

export default function (data) {
  const wallets = data.accountIDs;
  if (wallets.length < 2) {
    return;
  }

  const roll = Math.random();

  if (roll < 0.9) {
    const sourceIdx = Math.floor(Math.random() * wallets.length);
    let destIdx;
    if (HOT) {
      destIdx = Math.random() < 0.8 ? HOT_WALLET_INDEX : Math.floor(Math.random() * wallets.length);
      if (destIdx === sourceIdx) {
        destIdx = (sourceIdx + 1) % wallets.length;
      }
    } else {
      do {
        destIdx = Math.floor(Math.random() * wallets.length);
      } while (destIdx === sourceIdx);
    }

    const idemKey = `transfer-${uuidv4()}`;
    const res = http.post(
      `${BASE_URL}/v1/transfers`,
      JSON.stringify({
        source_account_id: wallets[sourceIdx],
        destination_account_id: wallets[destIdx],
        amount: 1000,
        currency: 'IDR',
      }),
      {
        headers: {
          ...headers,
          'Idempotency-Key': idemKey,
        },
      }
    );

    check(res, {
      'transfer succeeded': (r) => r.status === 201 || r.status === 422,
    });
  } else {
    const destIdx = Math.floor(Math.random() * wallets.length);
    const idemKey = `topup-${uuidv4()}`;
    const res = http.post(
      `${BASE_URL}/v1/topups`,
      JSON.stringify({
        destination_account_id: wallets[destIdx],
        amount: 10000,
        currency: 'IDR',
      }),
      {
        headers: {
          ...headers,
          'Idempotency-Key': idemKey,
        },
      }
    );

    check(res, {
      'topup succeeded': (r) => r.status === 201 || r.status === 422,
    });
  }

  sleep(0.05);
}

export function handleSummary(data) {
  const output = {
    mode: HOT ? 'hot-wallet' : 'mixed',
    timestamp: new Date().toISOString(),
    vus: VUS,
    duration: DURATION,
    metrics: {
      http_req_duration: data.metrics.http_req_duration?.values || {},
      http_reqs: data.metrics.http_reqs?.values || {},
      data_received: data.metrics.data_received?.values || {},
      data_sent: data.metrics.data_sent?.values || {},
    },
  };

  return {
    'stdout': textSummary(data, { indent: '  ', enableColors: true }),
    'results/summary.json': JSON.stringify(output, null, 2),
  };
}

function textSummary(data, opts) {
  let out = '\n=== Load Test Summary ===\n';
  out += `Mode: ${HOT ? 'hot-wallet' : 'mixed'}\n`;
  out += `VUs: ${VUS}, Duration: ${DURATION}\n\n`;

  if (data.metrics.http_req_duration) {
    const v = data.metrics.http_req_duration.values;
    out += 'HTTP Request Duration:\n';
    out += `  avg: ${v.avg?.toFixed(2) || 0}ms\n`;
    out += `  min: ${v.min?.toFixed(2) || 0}ms\n`;
    out += `  med: ${v.med?.toFixed(2) || 0}ms\n`;
    out += `  max: ${v.max?.toFixed(2) || 0}ms\n`;
    out += `  p(50): ${v['p(50)']?.toFixed(2) || 0}ms\n`;
    out += `  p(95): ${v['p(95)']?.toFixed(2) || 0}ms\n`;
    out += `  p(99): ${v['p(99)']?.toFixed(2) || 0}ms\n\n`;
  }

  if (data.metrics.http_reqs) {
    const v = data.metrics.http_reqs.values;
    out += `Total Requests: ${v.count || 0}\n`;
    out += `Request Rate: ${v.rate?.toFixed(2) || 0} req/s\n`;
  }

  return out;
}
