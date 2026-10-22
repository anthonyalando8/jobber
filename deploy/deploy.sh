#!/usr/bin/env bash
# Build and (re)start Prospector, applying any pending migrations safely.
#
# Run from anywhere after `git pull`:   bash deploy/deploy.sh
#
# - Fresh, empty database: migrations are applied directly.
# - Existing database with pending migrations: the app is stopped, a backup is taken and
#   verified by restoring it into a scratch database, then migrations run against that proof.
#   If migrating fails, the previous app container is started again (its schema is unchanged,
#   because each migration rolls back as a unit).
# - Nothing pending: the app is simply rebuilt and restarted.
set -euo pipefail

cd "$(dirname "$0")/.."

if [ ! -f .env.production ]; then
  echo "error: .env.production not found (copy .env.production.example and fill it in)" >&2
  exit 1
fi
chmod 600 .env.production

compose() { docker compose "$@"; }

echo "==> Building image"
compose build app

echo "==> Starting database and Redis"
compose up -d --wait db redis

echo "==> Checking schema"
if compose run --rm migrate -command check >/dev/null 2>&1; then
  echo "    schema is current"
else
  tables=$(compose exec -T db sh -c \
    'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc "SELECT count(*) FROM information_schema.tables WHERE table_schema = '"'"'public'"'"'"')
  if [ "${tables//[[:space:]]/}" = "0" ]; then
    echo "==> Empty database: applying all migrations"
    compose run --rm migrate -command apply
  else
    ts=$(date +%Y%m%d-%H%M%S)
    mkdir -p backups/pre-migrate
    archive="/backups/pre-migrate/prospector-$ts.dump"
    echo "==> Pending migrations on an existing database"
    echo "    stopping app so the backup matches the database exactly"
    compose stop app || true
    restore_app() {
      echo "!!  migration failed; restarting the previous app container" >&2
      compose start app || true
    }
    trap restore_app ERR
    echo "==> Taking and verifying backup: backups/pre-migrate/prospector-$ts.dump"
    compose run --rm migrate -command backup-verify -out "$archive"
    echo "==> Applying migrations"
    compose run --rm migrate -command apply -proof "$archive.verified.json"
    trap - ERR
  fi
fi

echo "==> Starting app"
compose up -d --wait app

addr=$(compose port app 8080)
if curl -fsS "http://${addr}/healthz" >/dev/null; then
  echo "==> Healthy on ${addr}"
else
  echo "error: app did not report healthy; see: docker compose logs --tail=100 app" >&2
  exit 1
fi
