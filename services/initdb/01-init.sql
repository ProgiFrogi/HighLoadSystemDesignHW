-- ====== КАТАЛОГ ======
CREATE TABLE IF NOT EXISTS restaurants (
    restaurant_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(200) NOT NULL,
    cuisine VARCHAR(100) NOT NULL,
    lat DOUBLE PRECISION,
    lon DOUBLE PRECISION,
    rating NUMERIC(3,2) DEFAULT 0.0,
    is_open BOOLEAN DEFAULT true,
    created_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS menu_items (
    menu_item_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    restaurant_id UUID NOT NULL REFERENCES restaurants(restaurant_id) ON DELETE CASCADE,
    name VARCHAR(200) NOT NULL,
    price INTEGER NOT NULL CHECK (price > 0),
    category VARCHAR(100) DEFAULT 'main',
    is_available BOOLEAN DEFAULT true,
    updated_at TIMESTAMPTZ DEFAULT now()
);

-- ====== ЗАКАЗЫ ======
CREATE TABLE IF NOT EXISTS orders (
    order_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    restaurant_id UUID NOT NULL REFERENCES restaurants(restaurant_id),
    user_id UUID NOT NULL DEFAULT '00000000-0000-0000-0000-000000000001',
    status VARCHAR(30) NOT NULL DEFAULT 'created',
    total_amount INTEGER NOT NULL CHECK (total_amount > 0),
    idempotency_key VARCHAR(100) UNIQUE NOT NULL,
    delivery_address JSONB,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS order_items (
    order_id UUID NOT NULL REFERENCES orders(order_id) ON DELETE CASCADE,
    menu_item_id UUID NOT NULL,
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    price_snapshot INTEGER NOT NULL,
    name_snapshot VARCHAR(200) NOT NULL,
    PRIMARY KEY (order_id, menu_item_id)
);

-- ====== OUTBOX ======
CREATE TABLE IF NOT EXISTS outbox_events (
    event_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_id UUID NOT NULL,
    event_type VARCHAR(100) NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now(),
    published BOOLEAN DEFAULT false
);

-- ====== ИНДЕКСЫ ======
CREATE INDEX IF NOT EXISTS idx_restaurants_cuisine ON restaurants(cuisine);
CREATE INDEX IF NOT EXISTS idx_restaurants_name ON restaurants(name);
CREATE INDEX IF NOT EXISTS idx_restaurants_open ON restaurants(is_open) WHERE is_open = true;
CREATE INDEX IF NOT EXISTS idx_menu_items_restaurant ON menu_items(restaurant_id, is_available);
CREATE INDEX IF NOT EXISTS idx_menu_items_name ON menu_items(name);
CREATE INDEX IF NOT EXISTS idx_orders_user ON orders(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_orders_restaurant ON orders(restaurant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_orders_idempotency ON orders(idempotency_key);
CREATE INDEX IF NOT EXISTS idx_outbox_published ON outbox_events(published, created_at);

-- ====== ТЕСТОВЫЕ ДАННЫЕ ======
INSERT INTO restaurants (restaurant_id, name, cuisine, lat, lon, rating, is_open) VALUES
    ('a0000000-0000-0000-0000-000000000001', 'Sushi Town', 'japanese', 55.7558, 37.6173, 4.8, true),
    ('a0000000-0000-0000-0000-000000000002', 'Pizza Planet', 'italian', 55.7600, 37.6120, 4.5, true),
    ('a0000000-0000-0000-0000-000000000003', 'Burger House', 'american', 55.7400, 37.6050, 4.2, true),
    ('a0000000-0000-0000-0000-000000000004', 'Ramen Club', 'japanese', 55.7700, 37.6200, 4.6, true),
    ('a0000000-0000-0000-0000-000000000005', 'Taco Fiesta', 'mexican', 55.7580, 37.6100, 4.3, true);

-- Блюда для Sushi Town
INSERT INTO menu_items (menu_item_id, restaurant_id, name, price, category, is_available) VALUES
    ('b0000000-0000-0000-0000-000000000001', 'a0000000-0000-0000-0000-000000000001', 'Филадельфия ролл', 450, 'rolls', true),
    ('b0000000-0000-0000-0000-000000000002', 'a0000000-0000-0000-0000-000000000001', 'Калифорния ролл', 380, 'rolls', true),
    ('b0000000-0000-0000-0000-000000000003', 'a0000000-0000-0000-0000-000000000001', 'Унаги маки', 520, 'rolls', true),
    ('b0000000-0000-0000-0000-000000000004', 'a0000000-0000-0000-0000-000000000001', 'Сашими лосось', 650, 'sashimi', true),
    ('b0000000-0000-0000-0000-000000000005', 'a0000000-0000-0000-0000-000000000001', 'Мисо суп', 250, 'soups', true),
    ('b0000000-0000-0000-0000-000000000006', 'a0000000-0000-0000-0000-000000000001', 'Темпура ролл', 420, 'rolls', true),
    ('b0000000-0000-0000-0000-000000000007', 'a0000000-0000-0000-0000-000000000001', 'Сяке маки', 350, 'rolls', true),
    ('b0000000-0000-0000-0000-000000000008', 'a0000000-0000-0000-0000-000000000001', 'Радуга ролл', 580, 'rolls', true);

-- Блюда для Pizza Planet
INSERT INTO menu_items (menu_item_id, restaurant_id, name, price, category, is_available) VALUES
    ('b0000000-0000-0000-0000-000000000009', 'a0000000-0000-0000-0000-000000000002', 'Маргарита', 450, 'pizza', true),
    ('b0000000-0000-0000-0000-000000000010', 'a0000000-0000-0000-0000-000000000002', 'Пепперони', 550, 'pizza', true),
    ('b0000000-0000-0000-0000-000000000011', 'a0000000-0000-0000-0000-000000000002', 'Четыре сыра', 600, 'pizza', true),
    ('b0000000-0000-0000-0000-000000000012', 'a0000000-0000-0000-0000-000000000002', 'Карбонара', 480, 'pasta', true),
    ('b0000000-0000-0000-0000-000000000013', 'a0000000-0000-0000-0000-000000000002', 'Цезарь', 380, 'salads', true);

-- Блюда для Burger House
INSERT INTO menu_items (menu_item_id, restaurant_id, name, price, category, is_available) VALUES
    ('b0000000-0000-0000-0000-000000000014', 'a0000000-0000-0000-0000-000000000003', 'Чизбургер', 350, 'burgers', true),
    ('b0000000-0000-0000-0000-000000000015', 'a0000000-0000-0000-0000-000000000003', 'Гамбургер', 300, 'burgers', true),
    ('b0000000-0000-0000-0000-000000000016', 'a0000000-0000-0000-0000-000000000003', 'Чикен бургер', 320, 'burgers', true),
    ('b0000000-0000-0000-0000-000000000017', 'a0000000-0000-0000-0000-000000000003', 'Картошка фри', 150, 'sides', true);

-- Блюда для Ramen Club
INSERT INTO menu_items (menu_item_id, restaurant_id, name, price, category, is_available) VALUES
    ('b0000000-0000-0000-0000-000000000018', 'a0000000-0000-0000-0000-000000000004', 'Тонкоцу рамен', 480, 'ramen', true),
    ('b0000000-0000-0000-0000-000000000019', 'a0000000-0000-0000-0000-000000000004', 'Шио рамен', 450, 'ramen', true),
    ('b0000000-0000-0000-0000-000000000020', 'a0000000-0000-0000-0000-000000000004', 'Мисо рамен', 460, 'ramen', true),
    ('b0000000-0000-0000-0000-000000000021', 'a0000000-0000-0000-0000-000000000004', 'Гёдза', 280, 'appetizers', true),
    ('b0000000-0000-0000-0000-000000000022', 'a0000000-0000-0000-0000-000000000004', 'Эдамаме', 200, 'appetizers', true);

-- Блюда для Taco Fiesta
INSERT INTO menu_items (menu_item_id, restaurant_id, name, price, category, is_available) VALUES
    ('b0000000-0000-0000-0000-000000000023', 'a0000000-0000-0000-0000-000000000005', 'Тако', 250, 'tacos', true),
    ('b0000000-0000-0000-0000-000000000024', 'a0000000-0000-0000-0000-000000000005', 'Буррито', 350, 'burritos', true),
    ('b0000000-0000-0000-0000-000000000025', 'a0000000-0000-0000-0000-000000000005', 'Кесадилья', 300, 'quesadillas', true),
    ('b0000000-0000-0000-0000-000000000026', 'a0000000-0000-0000-0000-000000000005', 'Начос', 280, 'nachos', true),
    ('b0000000-0000-0000-0000-000000000027', 'a0000000-0000-0000-0000-000000000005', 'Гуакамоле', 220, 'dips', true);