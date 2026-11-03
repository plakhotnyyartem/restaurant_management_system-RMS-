-- 005_promo.sql
-- Промокоды: скидка в процентах или фиксированной суммой на весь заказ.
-- Скрипт идемпотентный: его можно запускать повторно.
--
--   psql "postgres://postgres:postgres@localhost:5432/restaurant_management" \
--        -f backend/database/migrations/005_promo.sql

BEGIN;

CREATE TABLE IF NOT EXISTS promo_codes (
    id            SERIAL PRIMARY KEY,
    -- Код хранится в верхнем регистре: гость может ввести его как угодно.
    code          VARCHAR(32) NOT NULL UNIQUE
                  CHECK (code = upper(code) AND code ~ '^[A-Z0-9_-]{3,32}$'),
    kind          VARCHAR(10) NOT NULL CHECK (kind IN ('percent', 'fixed')),
    value         NUMERIC(10, 2) NOT NULL CHECK (value > 0),
    min_total     NUMERIC(10, 2) NOT NULL DEFAULT 0 CHECK (min_total >= 0),
    max_uses      INTEGER CHECK (max_uses IS NULL OR max_uses > 0),  -- NULL = без лимита
    once_per_user BOOLEAN NOT NULL DEFAULT TRUE,
    valid_until   DATE,                                              -- включительно; NULL = бессрочно
    is_active     BOOLEAN NOT NULL DEFAULT TRUE,
    description   TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (kind <> 'percent' OR value <= 90)
);

-- total_price заказа — уже со скидкой; discount — сколько списал промокод.
-- Использования не хранятся счётчиком, а считаются по заказам (кроме отменённых):
-- отмена заказа сама «возвращает» промокод.
ALTER TABLE orders ADD COLUMN IF NOT EXISTS promo_code_id INTEGER REFERENCES promo_codes (id);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS discount NUMERIC(10, 2) NOT NULL DEFAULT 0;
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'orders_discount_check') THEN
        ALTER TABLE orders ADD CONSTRAINT orders_discount_check CHECK (discount >= 0);
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS orders_promo_code_idx ON orders (promo_code_id) WHERE promo_code_id IS NOT NULL;

-- Демо-промокоды
INSERT INTO promo_codes (code, kind, value, min_total, description) VALUES
    ('WELCOME10', 'percent', 10, 0, 'Скидка 10% для новых гостей'),
    ('RMS500', 'fixed', 500, 3000, '500 ₸ при заказе от 3 000 ₸')
ON CONFLICT (code) DO NOTHING;

COMMIT;
