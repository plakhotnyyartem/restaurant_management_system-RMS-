-- 002_inventory.sql
-- Склад и техкарты для модуля оптимизации закупок.
-- Скрипт идемпотентный: его можно запускать повторно.
--
--   psql "postgres://postgres:postgres@localhost:5432/restaurant_management" \
--        -f backend/database/migrations/002_inventory.sql

BEGIN;

-- Продукты, которые закупает ресторан.
CREATE TABLE IF NOT EXISTS ingredients (
    id              SERIAL PRIMARY KEY,
    name            VARCHAR(100) NOT NULL UNIQUE,
    unit            VARCHAR(10)  NOT NULL,                  -- кг, л, шт
    price           NUMERIC(10, 2) NOT NULL CHECK (price >= 0),          -- ₸ за единицу
    pack_size       NUMERIC(10, 3) NOT NULL DEFAULT 1 CHECK (pack_size > 0), -- закупается кратно упаковке
    shelf_life_days INTEGER NOT NULL DEFAULT 30 CHECK (shelf_life_days > 0)
);

-- Техкарта: сколько продукта уходит на одну порцию блюда.
CREATE TABLE IF NOT EXISTS recipe_items (
    dish_id       INTEGER NOT NULL REFERENCES dishes (id) ON DELETE CASCADE,
    ingredient_id INTEGER NOT NULL REFERENCES ingredients (id),
    quantity      NUMERIC(10, 4) NOT NULL CHECK (quantity > 0),
    PRIMARY KEY (dish_id, ingredient_id)
);

-- Текущие остатки на складе.
CREATE TABLE IF NOT EXISTS stock (
    ingredient_id INTEGER PRIMARY KEY REFERENCES ingredients (id) ON DELETE CASCADE,
    quantity      NUMERIC(12, 3) NOT NULL DEFAULT 0 CHECK (quantity >= 0),
    updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS recipe_items_ingredient_idx ON recipe_items (ingredient_id);

COMMIT;
