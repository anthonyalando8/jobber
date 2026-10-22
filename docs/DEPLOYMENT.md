# Deployment runbook

Prospector runs on the VPS as one Docker Compose project, `prospector`, with its **own**
PostgreSQL 18 and Redis containers. It does not use the VPS's existing Postgres, Caddy or any other
application's containers. Only the app publishes a port, `127.0.0.1:18080`, which is unreachable
from the internet; you reach it through Tailscale (or an SSH tunnel).

| Container | Image | Port | Data |
| --- | --- | --- | --- |
| `app` | built from `Dockerfile` | `127.0.0.1:18080` | none (stateless) |
| `db` | `postgres:18-alpine` | none | volume `prospector_pgdata` |
| `redis` | `redis:7-alpine` | none | volume `prospector_redisdata` |
| `migrate` | same image as `app`, run on demand | none | `./backups` |

Every page, action and the live event stream require signing in with `ADMIN_ACCESS_KEY`. Only
`/login`, `/healthz` and `/static/*` are reachable without a session.

---

## 1. One-time VPS setup

### Check the port is free

```bash
ss -ltnp | grep 18080        # must print nothing; otherwise set PROSPECTOR_PORT (section 7)
```

### Get the code

If the GitHub repository is private, give the VPS a read-only deploy key:

```bash
ssh-keygen -t ed25519 -f ~/.ssh/prospector_deploy -N ""
cat ~/.ssh/prospector_deploy.pub   # GitHub → repo → Settings → Deploy keys → Add (read-only)
printf 'Host github-prospector\n  HostName github.com\n  IdentityFile ~/.ssh/prospector_deploy\n' >> ~/.ssh/config
```

```bash
sudo mkdir -p /opt/apps/prospector && sudo chown "$USER" /opt/apps/prospector
git clone git@github-prospector:anthonyalando8/jobber.git /opt/apps/prospector
# (public repo: git clone https://github.com/anthonyalando8/jobber.git /opt/apps/prospector)
cd /opt/apps/prospector
```

### Configure

```bash
cp .env.production.example .env.production
chmod 600 .env.production
openssl rand -base64 48      # → ADMIN_ACCESS_KEY
openssl rand -hex 24         # → POSTGRES_PASSWORD (also inside DATABASE_URL)
nano .env.production
```

Fill in at least `ADMIN_ACCESS_KEY`, `POSTGRES_PASSWORD`, the same password inside
`DATABASE_URL`, `ANTHROPIC_API_KEY` and the SMTP values. Use new credentials only.

### First deploy

```bash
bash deploy/deploy.sh
```

On an empty database this builds the image, starts `db` and `redis`, applies all migrations,
starts `app` and waits until `/healthz` answers `ok`. Then check:

```bash
curl -s 127.0.0.1:18080/healthz                 # ok
curl -sI 127.0.0.1:18080/companies | head -1     # 401 or 303: protected
docker compose ps                                # all healthy; only app shows 127.0.0.1:18080
docker compose logs -f app                       # worker loops start, discovery begins
```

### Private HTTPS access with Tailscale

```bash
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up
```

In the Tailscale admin console under **DNS**, enable **MagicDNS** and **HTTPS Certificates**. Then:

```bash
sudo tailscale serve --bg --https=443 http://127.0.0.1:18080
tailscale serve status       # shows https://<machine>.<tailnet>.ts.net
```

If port 443 clashes with Caddy, use `--https=8443` and browse to `https://<machine>.<tailnet>.ts.net:8443`.
Install the Tailscale app on your phone and laptop and sign in to the same account; the address
then works there with a valid certificate. Nothing is exposed publicly, and Caddy is untouched.

Without Tailscale, from a laptop: `ssh -N -L 18080:127.0.0.1:18080 user@vps`, then open
`http://localhost:18080`.

### Bring over your candidate profile

The production database starts with only the seeded profile revision 1. To reuse your local
active profile, export its document locally:

```powershell
psql "$env:DATABASE_URL" -tAc "SELECT document FROM candidate_profile_revision WHERE id = (SELECT active_revision_id FROM candidate_profile)" > profile.json
```

Then in production, open **Profile**, paste the JSON, save it as a draft, review it and activate
it. Delete `profile.json` afterwards. No other local data is needed: opportunities are rediscovered
by the workers.

### Nightly backups

```bash
crontab -e
# add:
45 3 * * * bash /opt/apps/prospector/deploy/backup.sh >> /opt/apps/prospector/backups/backup.log 2>&1
```

Dumps go to `backups/nightly/` and are kept for 14 days (`RETENTION_DAYS` overrides that). Copy
them off the VPS from time to time.

---

## 2. Updating

```bash
cd /opt/apps/prospector
git pull --ff-only
bash deploy/deploy.sh
```

`deploy.sh` decides what to do:

- **No pending migrations**: it rebuilds and restarts `app`. Compose stops the old container before
  starting the new one, so the worker loops never run twice.
- **Pending migrations**: it stops `app`, writes `backups/pre-migrate/prospector-<time>.dump`,
  verifies it by restoring into a scratch database, applies the migrations against that proof and
  starts the new `app`. If anything fails, it starts the previous `app` container again. A failed
  migration rolls back as a unit, so the old code still matches the schema.

---

## 3. Rollback and restore

**Code only** (no migration was applied):

```bash
git checkout <previous-commit>
docker compose up -d --build --wait app
```

**After a migration**: migrations are forward-only. Restore the pre-migration dump, then deploy the
previous code:

```bash
docker compose stop app
docker compose exec -T db sh -c 'dropdb -U "$POSTGRES_USER" --force "$POSTGRES_DB" && createdb -U "$POSTGRES_USER" "$POSTGRES_DB"'
docker compose exec -T db sh -c 'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --no-owner --exit-on-error' \
  < backups/pre-migrate/prospector-<time>.dump
git checkout <previous-commit>
docker compose up -d --build --wait app
```

Restoring a nightly dump works the same way, using a file from `backups/nightly/`.

---

## 4. Rotating the admin key

```bash
openssl rand -base64 48             # put the new value in ADMIN_ACCESS_KEY
docker compose up -d --wait app     # recreates the container with the new environment
```

Every existing session stops working at once, because each session is bound to a fingerprint of the
key that created it. After five wrong keys in 15 minutes, sign-in is locked until the window passes.
To clear the lockout immediately:

```bash
docker compose exec redis redis-cli DEL auth:login_failures
```

To sign out every device without changing the key:

```bash
docker compose exec redis sh -c "redis-cli --scan --pattern 'auth:session:*' | xargs -r redis-cli DEL"
```

---

## 5. Day-to-day commands

```bash
docker compose ps                                  # health of all containers
docker compose logs --tail=200 -f app              # application and worker logs
docker compose run --rm migrate -command check     # schema drift check
docker compose stats --no-stream                   # memory use against limits
bash deploy/backup.sh                              # backup now
```

Logs rotate at 10 MB × 3 files per container. Memory limits: app 256 MB, db 384 MB, redis 96 MB.
Measured at idle: about 14 MB, 49 MB and 4 MB.

---

## 6. Running locally

Local development is unchanged apart from one new required setting. Add `ADMIN_ACCESS_KEY` (32+
characters) to your `.env`, then run as before and sign in at `http://localhost:8080`. Browsers
accept the `Secure` session cookie on `localhost`. If yours does not, set `COOKIE_SECURE=false`
in `.env` (local only).

To try the production stack locally, create a `.env.production` from the example and run
`bash deploy/deploy.sh` (Git Bash on Windows works). It binds to `127.0.0.1:18080`.

---

## 7. Configuration notes

- `PROSPECTOR_PORT` (shell environment or a `.env` beside `docker-compose.yaml`) changes the
  loopback port from 18080.
- `TZ` defaults to `Africa/Nairobi`. Daily send and AI budgets reset at local midnight.
- Redis runs with `noeviction`: if it ever fills its 64 MB, writes fail loudly instead of dropping
  budgets or sessions.
- `.env.production` and `backups/` contain secrets and personal data. Both are git-ignored and
  excluded from the image.
