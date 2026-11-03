#!/bin/bash
# Готовит базу на хостинге (например, Neon): схема, все миграции, демо-данные
# и ваш аккаунт администратора.
#
#   bash scripts/deploy-db.sh
#
# Строка подключения и пароль вводятся скрыто и никуда не сохраняются.
# Повторный запуск безопасен: миграции идемпотентны, сид не дублирует заказы.
set -euo pipefail
cd "$(dirname "$0")/.."

read -r -s -p "Строка подключения к базе (postgresql://...): " DB_URL; echo
[[ "$DB_URL" == postgres* ]] || { echo "Это не похоже на строку подключения PostgreSQL"; exit 1; }

echo "→ Миграции"
for f in backend/database/migrations/0*.sql; do
  echo "   $(basename "$f")"
  psql "$DB_URL" -q -v ON_ERROR_STOP=1 -f "$f" 2>&1 | grep -v NOTICE || true
done
# Время заказов и аналитика по часам — по Алматы
psql "$DB_URL" -q -v ON_ERROR_STOP=1 -c "ALTER DATABASE \"$(psql "$DB_URL" -Atc 'select current_database()')\" SET timezone = 'Asia/Almaty'"

echo "→ Демо-данные (меню, 250 клиентов, ~15 000 заказов, склад)"
(cd backend && DATABASE_URL="$DB_URL" go run ./cmd/seed)

# Демо-сотрудники создаются с паролем password123 — на публичном сайте его
# нужно сменить на случайный, иначе войти под владельцем сможет любой.
psql "$DB_URL" -q -v ON_ERROR_STOP=1 <<'SQL'
CREATE EXTENSION IF NOT EXISTS pgcrypto;
UPDATE users SET password_hash = crypt(gen_random_uuid()::text, gen_salt('bf', 10))
WHERE email IN ('waiter@rms.kz', 'cook@rms.kz', 'owner@rms.kz');
SQL
echo "   пароли демо-сотрудников заменены на случайные"

echo "→ Ваш аккаунт администратора"
read -r -p "   Email: " ADMIN_EMAIL
read -r -p "   Имя: " ADMIN_NAME
read -r -s -p "   Пароль (мин. 6 символов, раскладка EN): " ADMIN_PW; echo
read -r -s -p "   Повторите пароль: " ADMIN_PW2; echo
[[ "$ADMIN_PW" == "$ADMIN_PW2" ]] || { echo "Пароли не совпали — запустите скрипт ещё раз"; exit 1; }
[[ ${#ADMIN_PW} -ge 6 ]] || { echo "Пароль короче 6 символов"; exit 1; }
psql "$DB_URL" -q -v ON_ERROR_STOP=1 -v email="$ADMIN_EMAIL" -v name="${ADMIN_NAME:-Admin}" -v pw="$ADMIN_PW" <<'SQL'
INSERT INTO users (name, email, password_hash, role)
VALUES (:'name', lower(:'email'), crypt(:'pw', gen_salt('bf', 10)), 'admin')
ON CONFLICT (email) DO UPDATE SET password_hash = EXCLUDED.password_hash, role = 'admin';
SQL
unset DB_URL ADMIN_PW ADMIN_PW2

echo "✓ База готова. Войдите на сайте как $ADMIN_EMAIL"
