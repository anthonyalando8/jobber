-- Prospector database schema
-- Run with: psql prospector < migrations/0001_init.sql
BEGIN;
CREATE TABLE company (
  id          bigserial primary key,
  name        text not null,
  domain      text unique,      -- normalised host, the dedupe key
  source      text not null,    -- clutch|google|jobboard|playstore|manual
  source_url  text,
  segment     text,             -- agency|incubator|hiring|sme|unknown
  score       int  not null default 0,
  status      text not null default 'new',
      -- new|enriched|drafted|approved|sent|replied|call|quoted|won|lost|skip
  location    text,
  notes       text,
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now()
);

CREATE TABLE contact (
  id bigserial primary key,
  company_id bigint not null references company(id) on delete cascade,
  kind text not null,        -- email|phone|whatsapp|form_url
  value text not null,
  person_name text,
  role text,
  quality int not null default 0,   -- named 3 > role-based 2 > info@ 1
  unique (company_id, kind, value)
);

CREATE TABLE signal (        -- the evidence a draft may cite
  id bigserial primary key,
  company_id bigint not null references company(id) on delete cascade,
  kind text not null,
      -- hiring_backend | no_checkout | payment_complaint | tech:flutter
      -- | tech:woocommerce | paybill_listed | portfolio_frontend_only
      -- | stale_site | app_reviews
  detail text not null,      -- VERBATIM quote or fact, max ~300 chars
  url text,
  created_at timestamptz not null default now()
);

CREATE TABLE page_cache (
  url text primary key,
  status int,
  body text,
  fetched_at timestamptz not null default now()
);

CREATE TABLE draft (
  id bigserial primary key,
  company_id bigint not null references company(id) on delete cascade,
  contact_id bigint references contact(id),
  channel text not null,     -- email|whatsapp|phone_script
  subject text,
  body text not null,
  evidence_used text,        -- which signal ids the model cited
  state text not null default 'pending',
      -- pending|approved|sent|discarded|needs_manual
  edited_by_human bool not null default false,
  created_at timestamptz not null default now()
);

CREATE TABLE send_log (
  id bigserial primary key,
  draft_id bigint not null references draft(id),
  company_id bigint not null references company(id),
  to_value text not null,
  channel text not null,
  sent_at timestamptz not null default now(),
  provider_response text,
  followup_1_due date,
  followup_2_due date,
  replied_at timestamptz
);

CREATE TABLE suppression (
  value text primary key,    -- email or domain that asked to stop
  reason text,
  created_at timestamptz not null default now()
);

CREATE TABLE template (
  id bigserial primary key,
  segment text not null,     -- agency|incubator|hiring|sme
  channel text not null,     -- email|linkedin_dm|whatsapp|phone_script
  role_filter text,          -- optional: job role for hiring segment
  subject text,
  body text not null,
  display_only bool not null default false,  -- LinkedIn/WhatsApp variants
  created_at timestamptz not null default now()
);

-- Indexes for performance
CREATE INDEX on company (status, score desc);
CREATE INDEX on company (segment);
CREATE INDEX on signal (company_id);
CREATE INDEX on send_log (sent_at);
CREATE INDEX on contact (company_id);
CREATE INDEX on draft (company_id, state);
CREATE INDEX on suppression (created_at);

-- Insert default templates
INSERT INTO template (segment, channel, subject, body, display_only) VALUES
('agency', 'email', 'Backend overflow work — Go / M-Pesa',
'Hi {{first_name}},

I came across {{company}} — {{observation}}.

I''m a backend engineer in Nairobi: Go, Postgres, M-Pesa integrations, APIs for mobile teams. I work as a subcontractor for agencies on fixed-price scopes — you keep the client relationship, I deliver the server side, one to three weeks per job.

{{hypothesis}} If a project ever needs payments done properly or an API your Flutter team can build against, I''d like to be the person you call. Worth a 15-minute call this week?

Anthony
+254 700 536 326 · antho-tech.netlify.app

Tell me to stop and I won''t write again.',
false),

('agency', 'linkedin_dm', '', 'Hi {{first_name}} — saw {{observation}}. I''m a Go/Postgres backend dev in Nairobi doing subcontract work for agencies: M-Pesa integrations and APIs for mobile teams, fixed price per scope. Do you ever outsource backend work? Happy to send a one-pager.', true),

('incubator', 'email', 'Backend support for cohort teams',
'Hi {{first_name}},

I''m Anthony, a backend engineer in Nairobi — {{observation}}.

Most early teams I meet hit the same wall: M-Pesa payments and a backend for their app, with no server-side engineer on the team. That''s exactly what I do, on short fixed-price scopes that fit a startup budget.

Would you be open to pointing me at two or three teams who are stuck there? Happy to do a free 30-minute review for any of them so they can judge the value first.

Anthony
+254 700 536 326 · anthonyalando8@gmail.com

Tell me to stop and I won''t write again.',
false),

('incubator', 'email', 'Payments on {{company}}',
'Hi {{first_name}},

{{observation}}.

I''m a backend engineer — Go, Postgres, M-Pesa. I''ve just finished designing the payments and wallet architecture for a mobility platform, so this is fresh: STK push, verified callbacks, no double charges, automatic reconciliation.

{{hypothesis}} If getting payments working is on your critical path, I can do it in 7 days for a fixed KSh 55,000, half upfront. Want a 15-minute call to see if it fits?

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won''t write again.',
false),

('hiring', 'email', 'Your {{role}} role — contract cover while you hire',
'Hi {{first_name}},

I saw you''re hiring a {{role}} — {{observation}}.

Filling that seat usually takes a couple of months. If something is blocked in the meantime, I can work on contract: Go, Postgres, Redis, M-Pesa integrations, APIs for mobile teams. Fixed price per scope, no notice period, and it doesn''t interfere with your hiring.

{{hypothesis}} Two questions and I''ll know if I can help: what''s the most urgent thing waiting on that hire, and when did you need it live?

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won''t write again.',
false),

('sme', 'email', 'Automating your payment confirmations',
'Hello {{first_name}},

I noticed {{observation}}.

I build small systems that remove that daily work: payments confirmed automatically and matched to the right customer, receipts sent on their own, and one simple report at the end of the day. It takes about a week and costs a fixed amount agreed before we start.

Would a quick call tomorrow be useful? I''ll tell you honestly whether it''s worth doing for a business your size.

Anthony Alando · +254 700 536 326

Tell me to stop and I won''t write again.',
false),

('sme', 'phone_script', '', 'Good morning, is this {{first_name}}? My name is Anthony, I build business systems here in Nairobi — I''m not selling anything on this call. I noticed {{observation}}. Can I ask, who currently checks that payments have come in, and how long does that take each day?', true);


COMMIT;