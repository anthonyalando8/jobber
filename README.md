# Prospector

A small Go service that finds Kenyan companies likely to need backend/payments work, enriches them with evidence, drafts personalized first-contact messages, and sends approved ones via Gmail.

- **Stack**: Go 1.22+ · Postgres · Redis · net/http · chi · html/template
- **No login, no multi-user, no roles**
- **No mass send — one message per click, throttled**
- **No React/SPA — server-rendered HTML + vanilla JS**
- **AI-agnostic** — works with Claude or Gemini (or easily add more providers)

## Quick Start

### Prerequisites
```bash
# Install Go 1.22+, Postgres, Redis
brew install go postgresql redis  # macOS
# or equivalent for your OS
```

### Setup
```bash
# Clone and enter directory
cd prospector

# Copy environment template and fill in your values
cp .env.example .env
# Edit .env with your Gmail app password, Claude API key, Postgres/Redis URLs

# Create database and run migrations
make db

# Start the server
make run
```

Server runs on `http://localhost:8080` (see HTTP_ADDR in .env).

## Project Structure

```
prospector/
  cmd/prospector/
    main.go              one binary: serve + background workers
  internal/
    store/               database layer (queries, migrations)
    seed/                seed-list adapters (job boards, Clutch, etc)
    enrich/              web crawler + contact/signal extraction
    score/               segmentation + scoring rules
    draft/               Claude API integration + template fill
    send/                SMTP send, throttle, log
    web/                 HTTP handlers + HTML templates
    queue/               Redis job queue (LIST + BRPOPLPUSH)
  go.mod / go.sum
  .env.example
  Makefile
  README.md
```

## Pipeline

### 1. SEED
Add companies from multiple sources (job boards, Clutch, manual CSV). Normalize domain, deduplicate via Redis, skip obvious non-prospects.

**Sources:**
- **jobboard** — brightermonday.co.ke, fuzu.com, myjobmag.co.ke (search "backend", "golang", etc)
- **clutch** — clutch.co and goodfirms.co (Kenya filter)
- **google** — Plain SERP for configured queries
- **playstore** — Kenyan apps (Finance/Shopping/Transport), low-star reviews
- **manual** — CSV paste

### 2. ENRICH
Per company: fetch home + 7 key pages (robots.txt, /about, /contact, /careers, /pricing). 
Extract:
- **Emails** — ranked by quality (named > role-based > info@)
- **Phones** — regex for Kenya +254 or 0
- **Tech signals** — WooCommerce, Shopify, WordPress, Firebase, Flutter, etc.
- **Checkout presence** — "add to cart", "checkout", "pay now", "lipa"
- **Paybill/Till numbers** — payment infrastructure indicators
- **Freshness** — copyright year, blog dates

Every signal stores verbatim detail + URL (no paraphrasing).

### 3. SCORE
Rule-based segmentation and scoring:
- **hiring** — job posting source → rank by seniority, currency, M-Pesa mention
- **agency** — Clutch/Goodfirms or "we build software" copy
- **incubator** — Incubator/accelerator mentions, innovation program language
- **sme** — Manual score + retail/small business signals

### 4. DRAFT
Claude API fills `{{observation}}` and `{{hypothesis}}` into fixed templates. No observation = no send.

### 5. SEND
Human reviews and approves. Click to send via Gmail SMTP. Throttled: 20/day max, 90s min between sends. Tracks send_log + followup dates.

## Environment Variables

```bash
# Database
DATABASE_URL=postgres://user:pass@localhost:5432/prospector?sslmode=disable

# Redis
REDIS_URL=redis://localhost:6379/0

# HTTP
HTTP_ADDR=:8080

# SMTP (Gmail with APP PASSWORD, not account password)
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_USER=anthonyalando8@gmail.com
SMTP_PASS=xxxx-xxxx-xxxx-xxxx
FROM_NAME=Anthony Alando
FROM_EMAIL=anthonyalando8@gmail.com
REPLY_TO=anthonyalando8@gmail.com

# AI Provider — Choose ONE (Claude preferred if both set)
# Claude (Anthropic)
ANTHROPIC_API_KEY=sk-ant-...
ANTHROPIC_MODEL=claude-sonnet-5

# OR Google Gemini (fallback if Claude not set)
GEMINI_API_KEY=...
GEMINI_MODEL=gemini-3.5-flash

# Guardrails (read at runtime)
DAILY_SEND_LIMIT=20
MIN_SEND_INTERVAL_SECONDS=90
CRAWL_DELAY_MS=1200
CRAWL_USER_AGENT=prospector/1.0 (+mailto:anthonyalando8@gmail.com)
```

**AI Provider Selection Logic:**
1. If `ANTHROPIC_API_KEY` is set → uses Claude
2. Else if `GEMINI_API_KEY` is set → uses Gemini
3. Else → errors (configure at least one)

## What Redis Is For

| Key | Use |
| --- | --- |
| `queue:enrich`, `queue:draft` | Job lists (BRPOPLPUSH for safety) |
| `crawl:host:{host}` | Per-host rate limit (SET NX EX 2) |
| `send:day:{YYYY-MM-DD}` | Daily send counter (INCR, refuses past limit) |
| `send:last_at` | Min interval between sends (looks human) |
| `seen:domain:{domain}` | Fast dupe check during seeding |

## UI Pages

- **Home** — Dashboard, quick stats, action buttons
- **Companies** — List (filter by status/segment), inline edit notes
- **Company detail** — All signals, contacts, score, templates for review
- **Draft review** — Side-by-side view of observation/hypothesis, objection handler sidebar
- **Admin** — Manual seed, trigger enrich/draft, send log

## API Endpoints

### GET
- `GET /` — Dashboard
- `GET /companies` — List companies (queryable by status, segment, score range)
- `GET /companies/{id}` — Company detail
- `GET /companies/{id}/draft` — View draft

### POST
- `POST /companies` — Add company manually (JSON or form)
- `POST /companies/{id}/draft/approve` — Approve/edit and store draft
- `POST /companies/{id}/send` — Send draft + log
- `POST /admin/seed/{source}` — Trigger seed adapter (jobboard|clutch|google|playstore|csv)
- `POST /admin/enrich` — Trigger enrichment for new companies
- `POST /admin/draft` — Trigger drafting for enriched companies

## Testing

Run acceptance checks:
```bash
make test
```

Checks:
1. Database connectivity and schema
2. Redis connectivity
3. API HTTP health

## Non-goals — We Reject These

- ❌ Login, multi-user, roles
- ❌ Mass/bulk send (one per click, throttled)
- ❌ LinkedIn scraping or automation
- ❌ CRM features, kanban, dashboards
- ❌ React/Vue/SPA (server-rendered HTML only)
- ❌ Docker orchestration, large test suite

**Why?** Every hour past timeboxes is an hour not spent on actual outreach.

## License

MIT
