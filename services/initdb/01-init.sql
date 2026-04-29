CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE IF NOT EXISTS restaurants (
    restaurant_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(200) NOT NULL,
    cuisine VARCHAR(100) NOT NULL,
    location GEOGRAPHY(Point, 4326),
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

CREATE TABLE IF NOT EXISTS outbox_events (
    event_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_id UUID NOT NULL,
    event_type VARCHAR(100) NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now(),
    published BOOLEAN DEFAULT false
);

CREATE INDEX IF NOT EXISTS idx_restaurants_cuisine ON restaurants(cuisine);
CREATE INDEX IF NOT EXISTS idx_restaurants_location ON restaurants USING GiST(location);
CREATE INDEX IF NOT EXISTS idx_restaurants_name_trgm ON restaurants USING GIN(name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_restaurants_open ON restaurants(is_open) WHERE is_open = true;

CREATE INDEX IF NOT EXISTS idx_menu_items_restaurant ON menu_items(restaurant_id, is_available);
CREATE INDEX IF NOT EXISTS idx_menu_items_name_trgm ON menu_items USING GIN(name gin_trgm_ops);

CREATE INDEX IF NOT EXISTS idx_orders_user ON orders(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_orders_restaurant ON orders(restaurant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_orders_idempotency ON orders(idempotency_key);

CREATE INDEX IF NOT EXISTS idx_outbox_published ON outbox_events(published, created_at);





-- ====== ТЕСТОВЫЕ ДАННЫЕ ======
DO $$
DECLARE
    rest_id1 UUID;
    rest_id2 UUID;
    rest_id3 UUID;
    rest_id4 UUID;
    rest_id5 UUID;
    i INTEGER;
BEGIN
    INSERT INTO restaurants (restaurant_id, name, cuisine, location, rating, is_open) VALUES
        (gen_random_uuid(), 'Sushi Town', 'japanese', ST_MakePoint(37.6173, 55.7558), 4.8, true)
        RETURNING restaurant_id INTO rest_id1;
    
    INSERT INTO restaurants (restaurant_id, name, cuisine, location, rating, is_open) VALUES
        (gen_random_uuid(), 'Pizza Planet', 'italian', ST_MakePoint(37.6120, 55.7600), 4.5, true)
        RETURNING restaurant_id INTO rest_id2;
    
    INSERT INTO restaurants (restaurant_id, name, cuisine, location, rating, is_open) VALUES
        (gen_random_uuid(), 'Burger House', 'american', ST_MakePoint(37.6050, 55.7400), 4.2, true)
        RETURNING restaurant_id INTO rest_id3;
    
    INSERT INTO restaurants (restaurant_id, name, cuisine, location, rating, is_open) VALUES
        (gen_random_uuid(), 'Ramen Club', 'japanese', ST_MakePoint(37.6200, 55.7700), 4.6, true)
        RETURNING restaurant_id INTO rest_id4;
    
    INSERT INTO restaurants (restaurant_id, name, cuisine, location, rating, is_open) VALUES
        (gen_random_uuid(), 'Taco Fiesta', 'mexican', ST_MakePoint(37.6100, 55.7580), 4.3, true)
        RETURNING restaurant_id INTO rest_id5;

    FOR i IN 1..15 LOOP
        INSERT INTO menu_items (menu_item_id, restaurant_id, name, price, category, is_available)
        VALUES (gen_random_uuid(), rest_id1, 
                CASE (i % 5)
                    WHEN 0 THEN 'Филадельфия ролл'
                    WHEN 1 THEN 'Калифорния ролл'
                    WHEN 2 THEN 'Унаги маки'
                    WHEN 3 THEN 'Сашими лосось'
                    WHEN 4 THEN 'Мисо суп'
                END || ' #' || i,
                200 + (random() * 800)::int,
                CASE (i % 5)
                    WHEN 0 THEN 'rolls'
                    WHEN 1 THEN 'rolls'
                    WHEN 2 THEN 'rolls'
                    WHEN 3 THEN 'sashimi'
                    WHEN 4 THEN 'soups'
                END,
                random() > 0.1);
    END LOOP;

    FOR i IN 1..15 LOOP
        INSERT INTO menu_items (menu_item_id, restaurant_id, name, price, category, is_available)
        VALUES (gen_random_uuid(), rest_id2,
                CASE (i % 5)
                    WHEN 0 THEN 'Маргарита'
                    WHEN 1 THEN 'Пепперони'
                    WHEN 2 THEN 'Четыре сыра'
                    WHEN 3 THEN 'Карбонара'
                    WHEN 4 THEN 'Цезарь'
                END || ' #' || i,
                300 + (random() * 600)::int,
                CASE (i % 5)
                    WHEN 0 THEN 'pizza'
                    WHEN 1 THEN 'pizza'
                    WHEN 2 THEN 'pizza'
                    WHEN 3 THEN 'pasta'
                    WHEN 4 THEN 'salads'
                END,
                random() > 0.1);
    END LOOP;

    FOR i IN 1..15 LOOP
        INSERT INTO menu_items (menu_item_id, restaurant_id, name, price, category, is_available)
        VALUES (gen_random_uuid(), rest_id3,
                CASE (i % 4)
                    WHEN 0 THEN 'Чизбургер'
                    WHEN 1 THEN 'Гамбургер'
                    WHEN 2 THEN 'Чикен бургер'
                    WHEN 3 THEN 'Картошка фри'
                END || ' #' || i,
                150 + (random() * 400)::int,
                CASE (i % 4)
                    WHEN 0 THEN 'burgers'
                    WHEN 1 THEN 'burgers'
                    WHEN 2 THEN 'burgers'
                    WHEN 3 THEN 'sides'
                END,
                random() > 0.1);
    END LOOP;

    FOR i IN 1..15 LOOP
        INSERT INTO menu_items (menu_item_id, restaurant_id, name, price, category, is_available)
        VALUES (gen_random_uuid(), rest_id4,
                CASE (i % 5)
                    WHEN 0 THEN 'Тонкоцу рамен'
                    WHEN 1 THEN 'Шио рамен'
                    WHEN 2 THEN 'Мисо рамен'
                    WHEN 3 THEN 'Гёдза'
                    WHEN 4 THEN 'Эдамаме'
                END || ' #' || i,
                250 + (random() * 500)::int,
                CASE (i % 5)
                    WHEN 0 THEN 'ramen'
                    WHEN 1 THEN 'ramen'
                    WHEN 2 THEN 'ramen'
                    WHEN 3 THEN 'appetizers'
                    WHEN 4 THEN 'appetizers'
                END,
                random() > 0.1);
    END LOOP;

    FOR i IN 1..15 LOOP
        INSERT INTO menu_items (menu_item_id, restaurant_id, name, price, category, is_available)
        VALUES (gen_random_uuid(), rest_id5,
                CASE (i % 5)
                    WHEN 0 THEN 'Тако'
                    WHEN 1 THEN 'Буррито'
                    WHEN 2 THEN 'Кесадилья'
                    WHEN 3 THEN 'Начос'
                    WHEN 4 THEN 'Гуакамоле'
                END || ' #' || i,
                180 + (random() * 450)::int,
                CASE (i % 5)
                    WHEN 0 THEN 'tacos'
                    WHEN 1 THEN 'burritos'
                    WHEN 2 THEN 'quesadillas'
                    WHEN 3 THEN 'nachos'
                    WHEN 4 THEN 'dips'
                END,
                random() > 0.1);
    END LOOP;
END $$;