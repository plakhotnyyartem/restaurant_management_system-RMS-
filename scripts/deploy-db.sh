#!/bin/bash
# Подготовка облачной базы (Neon и любой PostgreSQL):
#   1) все миграции по порядку, 2) часовой пояс Алматы,
#   3) демо-данные, 4) случайные пароли демо-сотрудникам (сайт будет публичным),
#   5) по желанию — сделать ваш аккаунт админом.
# Строка подключения вводится скрыто и никуда не сохраняется.
#
#   bash scripts/deploy-db.sh
set -euo pipefail
cd "$(dirname "$0")/.."

read -r -s -p "Строка подключения к облачной базе (postgresql://…): " DB_URL; echo
case "$DB_URL" in
  postgres://*|postgresql://*) ;;
  *) echo "Это не похоже на строку подключения PostgreSQL"; exit 1 ;;
esac
if [ "$DB_URL" = "$(grep '^DATABASE_URL=' backend/.env 2>/dev/null | cut -d= -f2-)" ]; then
  echo "Это строка от локальной базы, а нужна облачная"; exit 1
fi

echo "→ Проверка подключения"
if ! err=$(psql "$DB_URL" -qAtc "select 1" 2>&1 >/dev/null); then
  echo "Не удалось подключиться: $err" | sed -E 's#postgres(ql)?://[^ ]*#<строка подключения>#g'
  echo "Скопируйте строку заново (после сброса пароля старая не работает) и запустите скрипт ещё раз."
  exit 1
fi

echo "→ Миграции"
for f in backend/database/migrations/0*.sql; do
  echo "  ${f##*/}"
  # Останавливаемся на первой ошибке: следующие миграции зависят от предыдущих.
  if ! out=$(psql "$DB_URL" -q -v ON_ERROR_STOP=1 -f "$f" 2>&1); then
    echo "$out" | grep -v "NOTICE"
    echo "Миграция ${f##*/} не прошла, остальное не выполнялось."
    exit 1
  fi
done

echo "→ Часовой пояс базы: Asia/Almaty (как у сервера)"
psql "$DB_URL" -q -v ON_ERROR_STOP=1 -c "DO \$\$ BEGIN EXECUTE format('ALTER DATABASE %I SET timezone = %L', current_database(), 'Asia/Almaty'); END \$\$;"

echo "→ Демо-данные (меню, клиенты, ~15 000 заказов, склад)"
(cd backend && DATABASE_URL="$DB_URL" go run ./cmd/seed | sed 's/(пароль password123)/(пароль заменён ниже)/')

echo "→ Случайные пароли демо-сотрудникам: сайт публичный, password123 оставлять нельзя"
psql "$DB_URL" -q -v ON_ERROR_STOP=1 -c "CREATE EXTENSION IF NOT EXISTS pgcrypto" \
  -c "UPDATE users SET password_hash = crypt(encode(gen_random_bytes(18), 'base64'), gen_salt('bf', 10))
      WHERE email IN ('waiter@rms.kz', 'cook@rms.kz', 'owner@rms.kz')"

echo
read -r -p "Email, который сделать админом (сначала зарегистрируйтесь на сайте; Enter — пропустить): " ADMIN_EMAIL
if [ -n "$ADMIN_EMAIL" ]; then
  psql "$DB_URL" -q -v ON_ERROR_STOP=1 -v email="$ADMIN_EMAIL" <<'SQL'
UPDATE users SET role = 'admin' WHERE email = :'email';
SQL
  echo "Готово, если такой email уже зарегистрирован (иначе запустите скрипт ещё раз после регистрации)."
fi

unset DB_URL
echo "✓ База готова"
