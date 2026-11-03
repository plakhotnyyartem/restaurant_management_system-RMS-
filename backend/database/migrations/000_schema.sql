-- 000_schema.sql
-- Базовые таблицы: пользователи, категории, блюда, заказы. Запускается первой
-- на пустой базе; следующие миграции (001–005) дополняют эти таблицы.
-- Скрипт идемпотентный: на базе, где таблицы уже есть, ничего не меняет.
--
--   psql "postgres://postgres:postgres@localhost:5432/restaurant_management" \
--        -f backend/database/migrations/000_schema.sql

BEGIN;

CREATE TABLE IF NOT EXISTS users (
    id            SERIAL PRIMARY KEY,
    name          VARCHAR(100) NOT NULL,
    email         VARCHAR(150) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          VARCHAR(30) NOT NULL DEFAULT 'customer',
    created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS categories (
    id   SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS dishes (
    id           SERIAL PRIMARY KEY,
    name         VARCHAR(150) NOT NULL,
    description  TEXT,
    price        NUMERIC(10, 2) NOT NULL CHECK (price >= 0),
    category_id  INTEGER NOT NULL REFERENCES categories (id),
    image_url    TEXT,
    is_available BOOLEAN DEFAULT TRUE,
    created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS orders (
    id          SERIAL PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users (id),
    status      VARCHAR(30) NOT NULL DEFAULT 'pending',
    total_price NUMERIC(10, 2) NOT NULL DEFAULT 0,
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS order_items (
    id       SERIAL PRIMARY KEY,
    order_id INTEGER NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    dish_id  INTEGER NOT NULL REFERENCES dishes (id),
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    price    NUMERIC(10, 2) NOT NULL CHECK (price >= 0)
);

COMMIT;
