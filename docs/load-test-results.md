# Часть 1: Реализация PoC

## Архитектура
```
Client (k6 с хоста) → Nginx (:80)
                         ├── /api/v1/restaurants/* → catalog-service (:8081)
                         └── /api/v1/orders/*      → order-service (:8082)
                                                       ↓ HTTP (сетевое взаимодействие)
                                                 catalog-service
                                                       ↓
                                                 PostgreSQL (:5432) ← оба сервиса
                                                 Redis (:6379)      ← кеш, идемпотентность
```




Компонент |	Назначение |	Технология | 	Порт
--- | --- | --- | --- |
Nginx |	API Gateway, маршрутизация, rate limiting | 	nginx:1.25-alpine |	80
Catalog Service | 	Поиск ресторанов, меню (read-optimized) |	Go + chi router |	8081
Order Service |	Создание заказов, outbox, идемпотентность	| Go + chi router	| 8082
PostgreSQL |	Заказы, меню, outbox events |	PostgreSQL 16 Alpine	| 5432
Redis| 	Кеш меню, статусы заказов, идемпотентность |	Redis 7 Alpine |	6379


## Сетевое взаимодействие
 - Order Service -> Catalog Service: HTTP-запросы для валидации блюд перед созданием заказа

- Оба сервиса -> PostgreSQL: по TCP

 - Оба сервиса -> Redis: go-redis по TCP

 - Nginx -> сервисы: reverse proxy в Docker-сети foodnet

## API-операции (Happy path)

Метод | Эндпоинт |	Сервис |	Назначение
--- | --- | --- | --- |
`GET` |	/api/v1/restaurants/search?cuisine=...|	Catalog |	Поиск ресторанов
`GET` |	/api/v1/restaurants/{id}/menu |	Catalog |	Меню ресторана
`POST` |	/api/v1/orders |	Order |	Создание заказа
`GET` |	/api/v1/orders/{id} | 	Order |	Статус заказа

## База данных (PostgreSQL)
5 таблиц: restaurants, menu_items, orders, order_items, outbox_events

Индексы: BTREE, составные, partial indexes

Тестовые данные: 5 ресторанов, 27 блюд

## Асинхронное взаимодействие (Nice to have)
Transactional Outbox: события заказов пишутся в outbox_events в одной транзакции

Outbox Worker: фоновый воркер публикует события (PoC: логгирование; production: RabbitMQ/Kafka)

## Установленные лимиты
```
services:
  nginx:     cpus: "0.30", memory: 128M
  catalog:   cpus: "0.60", memory: 300M
  order:     cpus: "0.60", memory: 300M
  postgres:  cpus: "0.70", memory: 800M
  redis:     cpus: "0.20", memory: 128M
```
- p.s. Основной упор в CPU - памяти по итогу много остается, можно влить в бд, крч гошка по памяти прям радует

## Запуск
```
git clone https://github.com/ProgiFrogi/HighLoadSystemDesignHW.git
cd HighLoadSystemDesignHW
docker compose up -d
sleep 30
# На vm'ке тестируем
curl http://localhost/health # {"status":"ok"}
curl http://localhost/api/v1/restaurants/search?cuisine=japanese # 200 OK
```


# Часть 2: Паттерны
## Паттерны проектирования (Design Patterns)
### 1. API Gateway
- Где: services/nginx/default.conf

- Проблема: клиентам нужна единая точка входа, сокрытие внутренних портов, маршрутизация

- Решение: Nginx принимает все запросы на порт 80 и проксирует к нужному сервису
- Код(в nginx): proxy_pass http://catalog_upstream; (строка 27)

### 2. CQRS (Command Query Responsibility Segregation)
- Где: services/catalog/main.go (read) + services/order/main.go (write)

- Проблема: разная нагрузка и требования к оптимизации чтения и записи

- Решение: catalog-service оптимизирован под чтение (Redis Cache-Aside), order-service — под транзакционные записи (ACID, outbox)

- Код: catalog/main.go:1 (read-optimized), order/main.go:1 (write-optimized)

### 3. Transactional Outbox
- Где: services/order/main.go:360-380

- Проблема: события о создании заказа должны доставляться гарантированно, даже при сбоях

- Решение: запись в outbox_events в одной транзакции с заказом, фоновый worker публикует события

- Код: INSERT INTO outbox_events ... (строка 365), startOutboxWorker() (строка 430)

### 4. Cache-Aside
- Где: services/catalog/main.go:97-110

- Проблема: частые запросы меню ресторанов нагружают PostgreSQL

- Решение: проверка Redis → при промахе загрузка из БД → сохранение в Redis (TTL 5 минут)

- Код: rdb.Get(ctx, cacheKey) → db.Query(...) → rdb.Set(ctx, cacheKey, data, 5*time.Minute)

## Паттерны устойчивости (Resilience Patterns)
### 5. Circuit Breaker
- Где: services/order/main.go:125-138

- Проблема: если catalog-service недоступен, заказы не должны висеть и каскадно отказывать

- Решение: gobreaker размыкает цепь при 50%+ ошибок, быстрый возврат ошибки вместо зависания

- Код: gobreaker.NewCircuitBreaker(gobreaker.Settings{...}) (строка 125)

### 6. Timeout
- Где: services/order/main.go:230

- Проблема: зависшие HTTP-запросы к catalog-service блокируют горутины и соединения

- Решение: все исходящие запросы оборачиваются в context.WithTimeout(ctx, 3*time.Second)

- Код: reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second) (строка 230)

### 7. Retry with Backoff
- Где: services/order/main.go:430-480

- Проблема: временные сбои БД не должны терять outbox-события

- Решение: outbox worker опрашивает БД каждую секунду, повторяя неудачные попытки

- Код: for { time.Sleep(1 * time.Second); ... } (строка 440)

### 8. Idempotency Key
- Где: services/order/main.go:280-295

- Проблема: повторные POST-запросы (retry от клиента) не должны создавать дубликаты заказов

- Решение: проверка idempotency_key в PostgreSQL перед созданием, возврат существующего заказа

- Код: SELECT ... WHERE idempotency_key = $1 (строка 285)

### 9. Health Check
- Где: services/catalog/main.go:85, services/order/main.go:155

- Проблема: оркестратору (Docker Compose) нужно знать, жив ли сервис

- Решение: эндпоинт /health возвращает 200 + JSON с timestamp'ом

- Код: r.Get("/health", func(...) { json.NewEncoder(w).Encode({"status":"ok"}) })

# Часть 3: Итеративная оптимизация

## Профиль трафика: Mixed (70% read / 30% write)

## Методология тестирования

- **Инструмент:** k6 v1.6.1 (запуск с хоста, не с VM)
- **Сценарий:** 50 → 100 → 150 VU, 14 минут
- **Мониторинг USE:** docker stats на VM (CPU%, MEM USAGE)
- **Мониторинг RED:** вывод k6 (http_reqs, http_req_duration, http_req_failed)
- **Прогрев:** 2 минуты на низком RPS для прогрева кешей и пулов соединений
- Логи по каждой итерации можно найти в k6/result
---

## Таблица прогресса

| Метрика | NFR (ДЗ1) | Iter 0 | Iter 1 | Iter 3 |
|---------|-----------|--------|--------|--------|
| **RPS (общий)** | ≥130 | 727 | 1,103 | **1,322** |
| **RPS (read)** | ≥100 | 509 | 772 | **925** |
| **RPS (write)** | ≥30 | 218 | 331 | **397** |
| **p50 latency** | — | — | — | **8.9ms** |
| **p95 latency** | <500ms | 515ms ❌ | 316ms ✅ | **274ms** ✅ |
| **p99 latency** | <500ms | 894ms ❌ | 439ms ✅ | **398ms** ✅ |
| **Error rate** | <1% | 0.00% ✅ | 0.00% ✅ | **0.00%** ✅ |
| **CPU total (пик)** | 70-90% | 25-30% ❌ | 55-65% | **70-80%** ✅ |
| **RAM total (пик)** | — | 180 MB | 290 MB | **470 MB** |
| **Bottleneck** | — | Connection pool | PostgreSQL CPU | **Диск, тест** |
| **Что сделали** | — | Pool=2, без кеша, без индексов | Pool=15, индексы, идемпотентность | **Pool=20, Redis кеш, nginx кеш** |
| **NFR достигнут?** | — | ❌ (p95 > 500ms) | ✅ | ✅ |

---

## Iteration 0 — Baseline (деградация)

### Что сделали
- **Connection pool:** MaxConns=2, MinConns=1
- **Без Redis-кеша:** все запросы меню и статусов идут напрямую в PostgreSQL
- **Без проверки идемпотентности:** каждый POST-запрос создаёт новую транзакцию
- **Circuit Breaker агрессивный:** размыкается при 30% ошибок
- **HTTP таймауты:** 2 секунды

### USE метрики (пик при 100-150 VU)
- ps тут для контейнеров указан %, где всего 200% (как в логах), в total, где всего 100% (поделил на 2 сумму)

| Контейнер | CPU % | RAM |
|-----------|-------|-----|
| nginx | 10-15% | 12 MB |
| order-service | 12-15% | 13 MB |
| catalog-service | 13-18% | 9 MB |
| postgres | 15-18% | 180 MB |
| redis | 0.3% | 3 MB |
| **Total** | **25-30%** | **~220 MB** |

### RED метрики

| Метрика | Значение |
|---------|----------|
| RPS (общий) | **727 req/s** |
| RPS (read) | **509 req/s** |
| RPS (write) | **218 req/s** |
| p95 latency | **515ms** ❌ |
| p99 latency | **894ms** ❌ |
| Error rate | **0.00%** ✅ |

### Bottleneck анализ

**Основной bottleneck: Connection pool (2 соединения).**

CPU простаивает на 25-30% — система не загружена. Запросы к PostgreSQL 
ждут освобождения соединений в пуле. При 100+ VU очередь запросов растёт, 
latency увеличивается до 515ms (p95).

### Gap vs NFR

| NFR | Target | Actual | Gap |
|-----|--------|--------|-----|
| Read p99 | <500ms | ~800ms | +300ms ❌ |
| CPU util | 70-90% | 25-30% | -45% ❌ |

---

## Iteration 1 — Индексы + Connection Pool


### Что нашли (bottleneck из Iter 0)
- Connection pool = 2 — основной ограничитель производительности
- Отсутствие части индексов замедляет запросы поиска и меню
- Идемпотентность не проверяется — дублирующиеся заказы возможны при retry
- Circuit Breaker слишком агрессивный — размыкается при малейших задержках

### Что сделали
1. **Увеличили connection pool:** MaxConns 2->15, MinConns 1->5
2. **Создали индексы:**
   - `idx_restaurants_cuisine` — ускорение поиска по кухне
   - `idx_menu_items_restaurant` — ускорение загрузки меню
   - `idx_orders_idempotency` — быстрая проверка идемпотентности
   - `idx_orders_status` — выборка активных заказов
3. **Вернули проверку идемпотентности:** SELECT по `idempotency_key` перед INSERT
4. **Увеличили HTTP-таймауты:** 2s -> 3s
5. **Circuit Breaker:** менее агрессивный (50% ошибок, минимум 10 запросов)

### USE метрики (пик при 100-150 VU)

| Контейнер | CPU % | RAM |
|-----------|-------|-----|
| nginx | 20-27% | 9 MB |
| order-service | 22-27% | 27 MB |
| catalog-service | 27-35% | 17 MB |
| postgres | 40-47% | 290 MB |
| redis | 0.3% | 3 MB |
| **Total** | **55-65%** | **~350 MB** |

### RED метрики

| Метрика | Iter 0 | Iter 1 | Изменение |
|---------|--------|--------|-----------|
| RPS (общий) | 727 | **1,103** | **+52%** |
| RPS (read) | 509 | **772** | +52% |
| RPS (write) | 218 | **331** | +52% |
| p95 latency | 515ms | **316ms** | **-39%** |
| p99 latency | 894ms | **439ms** | -50,1% |
| Error rate | 0% | **0%** | — |

### Bottleneck анализ

**Bottleneck: PostgreSQL CPU (47%).**

После увеличения пула соединений запросы перестали ждать, и нагрузка 
переместилась на PostgreSQL. CPU вырос. Система стабильна, ошибок нет.

### NFR статус: ✅ Достигнут

---

## Iteration 3 — Кеширование (Redis + Nginx)

### Дата: 01.05.2026 14:25–14:39

### Что нашли (bottleneck из Iter 1)
- PostgreSQL CPU на 47% — каждый запрос меню идёт в БД
- Повторные запросы одинакового меню создают избыточную нагрузку
- Nginx не кеширует ответы GET-запросов
- Статусы заказов читаются из БД при каждом запросе

### Что сделали
1. **Включили Redis Cache-Aside для меню:**
   - Проверка Redis -> при промахе загрузка из PostgreSQL
   - Сохранение в Redis с TTL 5 минут
   - Hit rate: 62.5%
2. **Включили Redis-кеш статусов заказов:**
   - Ключ `order_status:{order_id}`, TTL 1 час
3. **Добавили Nginx proxy_cache для GET-запросов:**
   - TTL 10 секунд для поиска и меню
   - Кеш в памяти (10 MB)
4. **Увеличили connection pool:** 15 -> 20
5. **Оптимизировали Circuit Breaker:**
   - MaxRequests: 3 -> 5
   - Менее чувствительный триггер

### USE метрики (пик при 130-150 VU)

| Контейнер | CPU % | RAM |
|-----------|-------|-----|
| nginx | 25-30% | 9 MB |
| order-service | 27-31% | 31 MB |
| catalog-service | 28-32% | 21 MB |
| postgres | 35-40% | 325 MB |
| redis | 5-6% | 83 MB |
| **Total** | **70-80%** | **~470 MB** |

### Redis статистика
- **Keys:** 363,238
- **Hits:** 565,381
- **Misses:** 339,190
- **Hit rate:** 62.5%

### RED метрики

| Метрика | Iter 1 | Iter 3 | Изменение |
|---------|--------|--------|-----------|
| RPS (общий) | 1,103 | **1,322** | **+20%** |
| RPS (read) | 772 | **925** | +20% |
| RPS (write) | 331 | **397** | +20% |
| p95 latency | 316ms | **274ms** | **-13%** |
| p99 latency | 439ms | **398ms** | -9% |
| Error rate | 0% | **0%** | — |

### Bottleneck анализ

**Финальный bottleneck: HDD PostgreSQL.**

После добавления кешей CPU PostgreSQL снизился с 47% до 40%, но дисковая 
подсистема стала узким местом. При 150 VU рост RPS замедляется (плато), 
но деградации нет — 0% ошибок.

HDD медленнее SSD в 10-50x на random I/O. Замена на SSD позволит 
поднять RPS до 3000-5000 req/s.

Также можно попробовать усложнить тест (до 200-300 VUs)

### NFR статус: ✅ Достигнут с запасом

---

### Ключевые выводы

1. **Connection pool — самый дешёвый способ поднять RPS.** Увеличение с 2 до 15 дало +52% RPS без изменения кода.
2. **Индексы критичны для HDD.** Без индексов p95 latency > 500ms, с индексами — 316ms.
3. **Redis Cache-Aside эффективен.** 62.5% hit rate снизил нагрузку на PostgreSQL на 15% CPU.
4. **Nginx proxy_cache полезен для read-heavy эндпоинтов.** Даже 10 секунд TTL заметно разгружают бекенд.
5. **Circuit Breaker + Timeout + Idempotency = 0% ошибок.** Даже при пиковых нагрузках система стабильна.
6. **Go-сервисы очень эффективны.** 1322 RPS при 70-80% CPU на 2 vCPU — отличный результат.
