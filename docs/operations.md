# Deployment Document: Сервис доставки еды (PoC)

## 1. Архитектура

Назначение: PoC сервиса доставки еды с основными пользовательскими сценариями:

- поиск ресторанов;
- получение меню;
- создание заказа;
- просмотр статуса заказа.

Нагрузка (по `k6/loadtest/stress.js` и результатам в `k6/results`):

- профиль теста: `70% read / 30% write`;
- пиковая нагрузка теста: `50 -> 100 -> 150 VU`;
- достигнутый уровень: около `1322 RPS` (итерация 3), `0%` ошибок.

### 1.1 Описание развёртывания

#### Deployment diagram

Артефакт для сдачи: `docs/diagrams/deployment.png`.

#### Список сервисов

| Сервис | Реплики (PoC) | Ответственность | Тип | Зона |
|---|---:|---|---|---|
| Nginx | 1 | API Gateway, reverse proxy, rate limit, proxy cache | Stateless | Public |
| Catalog Service | 1 | Поиск ресторанов и выдача меню | Stateless | Private |
| Order Service | 1 | Создание заказов, idempotency, outbox worker (в БД) | Stateless | Private |
| PostgreSQL 16 | 1 | Основное хранилище (`restaurants`, `menu_items`, `orders`, `order_items`, `outbox_events`) | Stateful | Private |
| Redis 7 | 1 | Кэш меню, status cache, idempotency cache | Stateful | Private |

### 1.2 Стратегия деплоя

Для текущей реализации используется `docker-compose`, поэтому стратегии ниже указаны как эксплуатационные правила для PoC.

| Сервис | Стратегия | Почему |
|---|---|---|
| Nginx | Recreate | В PoC один инстанс, rolling без оркестратора невозможен. |
| Catalog Service | Recreate | Один инстанс в compose. |
| Order Service | Recreate | Один инстанс в compose. |
| PostgreSQL | Recreate в maintenance window | Stateful-компонент, обновляется только в окно. |
| Redis | Recreate в maintenance window | Stateful-кэш, допустима короткая деградация. |

#### Zero-downtime контракт

  - `/health` в `nginx`, `catalog`, `order`;
  - таймауты в HTTP-клиенте и middleware;
  - идемпотентность `POST /api/v1/orders`.

#### Миграции БД

- Сейчас: инициализация схемы SQL-скриптом `services/initdb/01-init.sql`.
- Для PoC достаточно пересоздания окружения.
- Для production-перехода нужен `Expand/Contract` (пока не реализовано в текущем коде).

### 1.3 Observability

#### Алерты (Golden Signals, critical path)

| Сигнал | Метрика | Порог | Окно | На что |
|---|---|---|---|---|
| Latency | p99 `POST /api/v1/orders` | `> 1300ms` | `5m` | Order Service |
| Errors | error rate `POST /api/v1/orders` | `> 1%` | `5m` | Nginx + Order Service |
| Traffic | RPS `POST /api/v1/orders` | `< 30%` от baseline | `10m` | Nginx |
| Saturation | `postgres active connections / max_connections` | `> 0.85` | `10m` | PostgreSQL |

#### Дашборды (3 уровня)

- **Overview**: RPS, p95/p99, error rate, доступность `/health` всех сервисов.
- **Service-level**: RED отдельно для `nginx`, `catalog`, `order`.
- **Diagnostic**: CPU/MEM/IO контейнеров, pool utilization Postgres/Redis, outbox lag.

#### Логи

- **Текущий формат в PoC**: текстовые stdout-логи Go (`chi/middleware.Logger`) и Nginx access logs.
- **Обязательные поля для production-версии**: `timestamp`, `level`, `service`, `request_id`, `route`, `status_code`, `latency_ms`, `error`.
- **Логируем**: входящие запросы, ошибки, outbox-публикацию.
- **Не логируем**: PII, секреты, платежные данные.

---

## 2. Доступность

### Целевая доступность

Цель для текущего PoC: **99.5%**.

Обоснование: single-instance контур в `docker-compose` без multi-AZ и без failover не позволяет честно заявлять 99.9%+.

### RPO/RTO по критичным компонентам

| Компонент | RPO | RTO | Комментарий |
|---|---:|---:|---|
| PostgreSQL | <= 24ч | <= 4ч | В PoC нет HA-реплик, восстановление из backup/snapshot. |
| Redis | <= 1ч | <= 1ч | Кэш восстанавливается прогревом, часть ключей теряется. |
| Очередь | N/A | N/A | Отдельного брокера в текущей реализации нет. |

### Стратегия резервирования

- Текущий PoC: active/single для всех компонентов (резервирование не реализовано).
- Минимальная production-стратегия:
  - PostgreSQL: active/standby + регулярные бэкапы;
  - Redis: master/replica;
  - сервисы: минимум 2 реплики за LB.

### Maintenance window

- Окно: воскресенье `02:00-04:00 (UTC+3)`, 1 раз в месяц.
- В этот период допускается краткий downtime PoC-кластера.

---

## 3. TCO в Yandex Cloud

Расчет сделан только по следующим компонентам:

- compute для `nginx + catalog + order` (VM);
- PostgreSQL;
- Redis;
- network egress.

Не включены: Kafka, отдельные сервисы Payment/Tracking/Notification, Object Storage, ALB.

### 3.1 Маппинг PoC на YC

| Компонент PoC | Сервис YC |
|---|---|
| Nginx + 2 Go-сервиса | Compute Cloud (VM) |
| PostgreSQL 16 | Managed Service for PostgreSQL |
| Redis 7 | Managed Service for Redis |
| Внешний трафик | Network egress |

### 3.2 Допущения

- Профиль нагрузки: базовый PoC из `k6`, масштабирование в сценариях `1x/2x/5x`.
- Compute:
  - 1x: 2 VM (основная + standby), каждая `4 vCPU / 8 GB RAM`;
  - 2x: 2 VM, каждая `8 vCPU / 16 GB RAM`;
  - 5x: 3 VM, каждая `8 vCPU / 32 GB RAM`.
- PostgreSQL:
  - 1x: 1 хост `4 vCPU / 16 GB`, SSD 200 GB;
  - 2x: 1 хост `8 vCPU / 32 GB`, SSD 400 GB;
  - 5x: 2 хоста `8 vCPU / 32 GB`, SSD 1 TB.
- Redis:
  - 1x: `2 vCPU / 8 GB`;
  - 2x: `4 vCPU / 16 GB`;
  - 5x: `8 vCPU / 32 GB`.
- Egress:
  - 1x: 75 TB/мес;
  - 2x: 150 TB/мес;
  - 5x: 375 TB/мес.

### 3.3 Инфраструктура, ₽/мес

| Статья | 1x | 2x | 5x |
|---|---:|---:|---:|
| Compute Cloud (VM для gateway+services) | 15 000 | 30 000 | 70 000 |
| Managed PostgreSQL (compute + storage + backup) | 35 000 | 55 000 | 150 000 |
| Managed Redis | 10 000 | 17 000 | 40 000 |
| Network egress | 80 000 | 150 000 | 375 000 |
| **Итого инфраструктура** | **140 000** | **252 000** | **635 000** |

### 3.4 Операционные затраты, ₽/мес

| Операционные затраты | 1x | 2x | 5x |
|---|---:|---:|---:|
| On-call и инциденты | 80 000 | 120 000 | 220 000 |
| Обслуживание и обновления | 40 000 | 60 000 | 100 000 |
| **Итого** | **120 000** | **180 000** | **320 000** |

### 3.5 Полный TCO (infra + Операционные затраты), ₽/мес

| Сценарий | Инфраструктура | Операционные затраты | Итого TCO |
|---|---:|---:|---:|
| 1x | 140 000 | 120 000 | **260 000** |
| 2x | 252 000 | 180 000 | **432 000** |
| 5x | 635 000 | 320 000 | **955 000** |

Комментарий по структуре затрат:

- На всех сценариях самые дорогие затраты — `network egress` и `Managed PostgreSQL`.
- `Network egress` доминирует из-за read-heavy профиля (большой объем ответов клиентам).
- `Managed PostgreSQL` дорогая из-за роста compute/storage/backup при масштабировании.
