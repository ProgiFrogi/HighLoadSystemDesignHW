import http from 'k6/http';
import { check, sleep } from 'k6';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';

export let options = {
    stages: [
        { duration: '2m', target: 20 },   // прогрев
        { duration: '5m', target: 50 },   // рабочая нагрузка
        { duration: '5m', target: 100 },  // ищем предел
        { duration: '5m', target: 150 },  // spike
        { duration: '2m', target: 0 },    // cool down
    ],
    thresholds: {
        'http_req_duration{type:read}': ['p(95)<500', 'p(99)<1000'],
        'http_req_duration{type:write}': ['p(95)<1000', 'p(99)<2000'],
        'errors{type:read}': ['rate<0.01'],
        'errors{type:write}': ['rate<0.01'],
    },
};

const BASE_URL = 'http://localhost';
const RESTAURANT_IDS = [
    '4d5b9a3c-1f2e-4d8b-9c7a-6f3e8d2a1b4c', // обновите после первого запуска
    '7f8e3d2c-5a4b-4c9d-8e1f-2a3b4c5d6e7f',
    '2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7f',
];

// Получим ID ресторанов вручную через curl и обновим этот файл
// curl http://localhost/api/v1/restaurants/search?cuisine=japanese | jq '.items[].restaurant_id'

export default function () {
    let headers = {
        'Content-Type': 'application/json',
        'User-Agent': 'k6-load-test',
    };

    let rand = Math.random();

    // 70% read: поиск ресторанов
    if (rand < 0.5) {
        let cuisines = ['japanese', 'italian', 'american', 'mexican'];
        let cuisine = cuisines[Math.floor(Math.random() * cuisines.length)];

        let res = http.get(`${BASE_URL}/api/v1/restaurants/search?cuisine=${cuisine}&limit=10`, {
            headers,
            tags: { type: 'read' },
        });

        check(res, {
            'search status 200': (r) => r.status === 200,
            'search has items': (r) => {
                try {
                    let body = JSON.parse(r.body);
                    return body.items && body.items.length > 0;
                } catch (e) {
                    return false;
                }
            },
        });
    }
    // 20% read: меню ресторана
    else if (rand < 0.7) {
        let restId = RESTAURANT_IDS[Math.floor(Math.random() * RESTAURANT_IDS.length)];

        let res = http.get(`${BASE_URL}/api/v1/restaurants/${restId}/menu`, {
            headers,
            tags: { type: 'read' },
        });

        check(res, {
            'menu status 200': (r) => r.status === 200,
            'menu has items': (r) => {
                try {
                    let body = JSON.parse(r.body);
                    return body.items && body.items.length > 0;
                } catch (e) { return false; }
            },
        });
    }
    // 10% write: создание заказа
    else {
        let idemKey = uuidv4();
        let restId = RESTAURANT_IDS[Math.floor(Math.random() * RESTAURANT_IDS.length)];

        let payload = JSON.stringify({
            restaurant_id: restId,
            items: [
                { menu_item_id: 'placeholder', quantity: 1 }
            ],
            delivery_address: {
                city: 'Москва',
                street: 'Тверская, 1',
            },
        });

        let res = http.post(`${BASE_URL}/api/v1/orders`, payload, {
            headers: {
                ...headers,
                'Idempotency-Key': idemKey,
            },
            tags: { type: 'write' },
        });

        check(res, {
            'order status 201 or 200': (r) => r.status === 201 || r.status === 200,
            'order has id': (r) => {
                try {
                    let body = JSON.parse(r.body);
                    return body.order_id && body.order_id.length > 0;
                } catch (e) { return false; }
            },
        });
    }

    sleep(0.1);
}

export function setup() {
    console.log('Getting restaurant IDs...');
    let res = http.get(`${BASE_URL}/api/v1/restaurants/search?cuisine=japanese`);
    if (res.status === 200) {
        let body = JSON.parse(res.body);
        if (body.items && body.items.length > 0) {
            console.log(`Found ${body.items.length} restaurants`);
            // Сохраняем ID для использования в тесте
            RESTAURANT_IDS.length = 0;
            body.items.forEach(item => RESTAURANT_IDS.push(item.restaurant_id));
        }
    }
    return { restaurant_ids: RESTAURANT_IDS };
}