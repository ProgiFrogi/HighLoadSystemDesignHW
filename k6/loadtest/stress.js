import http from 'k6/http';
import { check, sleep } from 'k6';

const MENU_BY_RESTAURANT = {
    'a0000000-0000-0000-0000-000000000001': [ // Sushi Town
        'b0000000-0000-0000-0000-000000000002',
        'b0000000-0000-0000-0000-000000000008',
        'b0000000-0000-0000-0000-000000000007',
        'b0000000-0000-0000-0000-000000000006',
        'b0000000-0000-0000-0000-000000000003',
        'b0000000-0000-0000-0000-000000000001',
        'b0000000-0000-0000-0000-000000000004',
        'b0000000-0000-0000-0000-000000000005',
    ],
    'a0000000-0000-0000-0000-000000000002': [ // Pizza Planet
        'b0000000-0000-0000-0000-000000000012',
        'b0000000-0000-0000-0000-000000000009',
        'b0000000-0000-0000-0000-000000000010',
        'b0000000-0000-0000-0000-000000000011',
        'b0000000-0000-0000-0000-000000000013',
    ],
    'a0000000-0000-0000-0000-000000000003': [ // Burger House
        'b0000000-0000-0000-0000-000000000015',
        'b0000000-0000-0000-0000-000000000014',
        'b0000000-0000-0000-0000-000000000016',
        'b0000000-0000-0000-0000-000000000017',
    ],
    'a0000000-0000-0000-0000-000000000004': [ // Ramen Club
        'b0000000-0000-0000-0000-000000000021',
        'b0000000-0000-0000-0000-000000000022',
        'b0000000-0000-0000-0000-000000000020',
        'b0000000-0000-0000-0000-000000000018',
        'b0000000-0000-0000-0000-000000000019',
    ],
    'a0000000-0000-0000-0000-000000000005': [ // Taco Fiesta
        'b0000000-0000-0000-0000-000000000024',
        'b0000000-0000-0000-0000-000000000027',
        'b0000000-0000-0000-0000-000000000026',
        'b0000000-0000-0000-0000-000000000025',
        'b0000000-0000-0000-0000-000000000023',
    ],
};

const ALL_RESTAURANTS = Object.keys(MENU_BY_RESTAURANT);

export let options = {
    stages: [
        { duration: '2m', target: 50 },   // прогрев гоев
        { duration: '5m', target: 100 },   // рабочая нагрузка
        { duration: '5m', target: 150 },  // терпим
        { duration: '2m', target: 0 },    // чилл
    ],
    thresholds: {
        'http_req_duration': ['p(95)<2000', 'p(99)<3000'],
        'http_req_failed': ['rate<0.01'],
    },
};

const BASE_URL = 'http://178.236.25.152';

export default function () {
    let headers = { 'Content-Type': 'application/json' };
    let rand = Math.random();

    // 50% — поиск ресторанов (READ)
    if (rand < 0.5) {
        let cuisines = ['japanese', 'italian', 'american', 'mexican'];
        let cuisine = cuisines[Math.floor(Math.random() * cuisines.length)];
        let res = http.get(`${BASE_URL}/api/v1/restaurants/search?cuisine=${cuisine}&limit=10`, { headers });
        check(res, { 'search 200': (r) => r.status === 200 });
    }
    // 20% — меню ресторана (READ)
    else if (rand < 0.7) {
        let restId = ALL_RESTAURANTS[Math.floor(Math.random() * ALL_RESTAURANTS.length)];
        let res = http.get(`${BASE_URL}/api/v1/restaurants/${restId}/menu`, { headers });
        check(res, { 'menu 200': (r) => r.status === 200 });
    }
    // 30% — создание заказа (WRITE)
    else {
        let restId = ALL_RESTAURANTS[Math.floor(Math.random() * ALL_RESTAURANTS.length)];
        let menuIds = MENU_BY_RESTAURANT[restId];
        let menuId = menuIds[Math.floor(Math.random() * menuIds.length)];

        let idemKey = `${Date.now()}-${Math.random()}`;
        let payload = JSON.stringify({
            restaurant_id: restId,
            items: [{ menu_item_id: menuId, quantity: 1 }],
            delivery_address: { city: 'Москва', street: 'Тверская, 1' },
        });

        let res = http.post(`${BASE_URL}/api/v1/orders`, payload, {
            headers: { ...headers, 'Idempotency-Key': idemKey },
        });
        check(res, { 'order 201/200': (r) => r.status === 201 || r.status === 200 });
    }
}

export function handleSummary(data) {
    let m = data.metrics;

    let rps = m.http_reqs.values.rate;

    let errorRate = m.http_req_failed.values.rate * 100;

    let p95 = m.http_req_duration.values['p(95)'];
    let p99 = m.http_req_duration.values['p(99)'] || 0;
    let total = m.http_reqs.values.count;

    console.log('\n=== СВОДКА BASELINE ===');
    console.log('Всего запросов: ' + total);
    console.log('RPS: ' + Number(rps).toFixed(1));
    console.log('Ошибок: ' + Number(errorRate).toFixed(2) + '%');
    console.log('p95: ' + Number(p95).toFixed(1) + 'ms');
    console.log('p99: ' + Number(p99).toFixed(1) + 'ms');

    return {};
}
