-- 001_analytics.sql
-- Подготовка схемы к аналитике и модулю поддержки решений.
-- Скрипт идемпотентный: его можно запускать повторно.
--
--   psql "postgres://postgres:postgres@localhost:5432/restaurant_management" \
--        -f backend/database/migrations/001_analytics.sql

BEGIN;

-- Себестоимость блюда (продукты на одну порцию). Нужна, чтобы считать маржу.
ALTER TABLE dishes
    ADD COLUMN IF NOT EXISTS cost_price NUMERIC(10, 2) NOT NULL DEFAULT 0;

-- Себестоимость фиксируется в позиции заказа так же, как цена:
-- если завтра продукты подорожают, прошлая маржа не «поплывёт».
ALTER TABLE order_items
    ADD COLUMN IF NOT EXISTS cost_price NUMERIC(10, 2) NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'dishes_cost_price_check') THEN
        ALTER TABLE dishes ADD CONSTRAINT dishes_cost_price_check CHECK (cost_price >= 0);
    END IF;

    -- Только статусы из жизненного цикла заказа.
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'orders_status_check') THEN
        ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK (
            status IN ('pending', 'confirmed', 'preparing', 'ready', 'completed', 'cancelled')
        );
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'users_role_check') THEN
        ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (
            role IN ('customer', 'waiter', 'cook', 'admin', 'owner')
        );
    END IF;
END $$;

-- Индексы под аналитические запросы (фильтр по дате, соединения по заказу и блюду).
CREATE INDEX IF NOT EXISTS orders_created_at_idx ON orders (created_at);
CREATE INDEX IF NOT EXISTS orders_user_id_idx ON orders (user_id);
CREATE INDEX IF NOT EXISTS orders_status_idx ON orders (status);
CREATE INDEX IF NOT EXISTS order_items_order_id_idx ON order_items (order_id);
CREATE INDEX IF NOT EXISTS order_items_dish_id_idx ON order_items (dish_id);
CREATE INDEX IF NOT EXISTS dishes_category_id_idx ON dishes (category_id);

COMMIT;
