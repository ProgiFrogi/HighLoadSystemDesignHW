import http from 'k6/http';
import { check } from 'k6';

export let options = { vus: 1, duration: '5s' };

export default function () {
    let res = http.get('http://178.236.25.152/health');
    check(res, { 'status 200': (r) => r.status === 200 });
}

export function handleSummary(data) {
    console.log('\n=== DEBUG DATA STRUCTURE ===');
    console.log('Keys:', Object.keys(data));
    console.log('Metrics keys:', Object.keys(data.metrics || {}));
    console.log('http_reqs:', JSON.stringify(data.metrics?.http_reqs));
    console.log('http_req_failed:', JSON.stringify(data.metrics?.http_req_failed));
    console.log('http_req_duration:', JSON.stringify(data.metrics?.http_req_duration));
    
    return {};
}
