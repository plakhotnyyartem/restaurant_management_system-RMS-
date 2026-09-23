-- 003_telegram.sql
-- Telegram-бот: привязка аккаунтов, одноразовые коды привязки, настройки бота.
-- Скрипт идемпотентный: его можно запускать повторно.
--
--   psql "postgres://postgres:postgres@localhost:5432/restaurant_management" \
--        -f backend/database/migrations/003_telegram.sql

BEGIN;

-- Аккаунт сайта ↔ пользователь Telegram (один к одному).
-- telegram_id — id пользователя в Telegram; в личном чате с ботом он же id чата.
CREATE TABLE IF NOT EXISTS telegram_links (
    user_id     INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    telegram_id BIGINT NOT NULL UNIQUE,
    username    VARCHAR(100),
    digest      BOOLEAN NOT NULL DEFAULT TRUE,   -- утренняя сводка (для владельца и админа)
    linked_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Одноразовые коды из ссылки t.me/<бот>?start=<код>. Живут 15 минут.
CREATE TABLE IF NOT EXISTS telegram_link_codes (
    code       VARCHAR(64) PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TIMESTAMP NOT NULL
);

-- Настройки бота: чат кухни, дата последней утренней сводки и т. п.
CREATE TABLE IF NOT EXISTS bot_settings (
    key   VARCHAR(50) PRIMARY KEY,
    value TEXT NOT NULL
);

COMMIT;
