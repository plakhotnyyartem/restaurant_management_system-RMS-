#!/bin/bash
# Делает зарегистрированного пользователя админом в любой базе (например, в облачной).
# Строка подключения вводится скрыто и никуда не сохраняется.
#
#   bash scripts/make-admin.sh you@example.com
set -euo pipefail

EMAIL="${1:-}"
if [ -z "$EMAIL" ]; then
  read -r -p "Email пользователя: " EMAIL
fi

read -r -s -p "Строка подключения к базе (postgresql://…): " DB_URL; echo
case "$DB_URL" in
  postgres://*|postgresql://*) ;;
  *) echo "Это не похоже на строку подключения PostgreSQL"; exit 1 ;;
esac

if ! updated=$(psql "$DB_URL" -qAt -v ON_ERROR_STOP=1 -v email="$EMAIL" 2>&1 <<'SQL'
WITH changed AS (
    UPDATE users SET role = 'admin' WHERE lower(email) = lower(:'email') RETURNING email
)
SELECT count(*) FROM changed;
SQL
); then
  echo "Не удалось подключиться или выполнить запрос:"
  echo "$updated" | sed -E 's#postgres(ql)?://[^ ]*#<строка подключения>#g'
  exit 1
fi
unset DB_URL

if [ "$updated" = "1" ]; then
  echo "✓ $EMAIL теперь админ. Выйдите и войдите на сайте заново, чтобы появилась «Админка»."
else
  echo "Пользователь $EMAIL не найден: сначала зарегистрируйтесь на сайте с этой почтой."
  exit 1
fi
