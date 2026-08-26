-- Tone pass across every cold-outreach template: the biggest recurring "this was clearly
-- AI-written" tell was a comma-spliced feature list dressed up as a sentence ("STK push,
-- verified callbacks, no double charges, automatic reconciliation") — a spec sheet, not
-- something a person would actually write in an email. Rewritten as connected prose throughout.
-- Also dropped pitch-deck phrasing ("Where I come in:") for plainer speech, and softened a few
-- corporate-sounding qualifiers ("Fixed price per scope... it doesn't interfere with your
-- hiring" -> "Fixed price... it won't get in the way"). The sme/incubator "programme manager"
-- templates and the follow-up/referral messages were already close to this register and are
-- left untouched.

UPDATE template
SET body = 'Hi {{first_name}},

I came across {{company}} — {{observation}}.

I''m a backend engineer in Nairobi (Go, Postgres, M-Pesa), and I take on subcontract work for agencies. You keep the client relationship, I handle the server side — usually one to three weeks per job, fixed price. What I''d actually help with: {{service_pitch}}.

{{hypothesis}} Worth a quick 15-minute call if something like that comes up?

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won''t write again.'
WHERE segment = 'agency' AND channel = 'email' AND display_only = false;

UPDATE template
SET body = 'Hi {{first_name}},

I saw you''re hiring a {{role}} — {{observation}}.

Filling that seat usually takes a couple of months. If something''s blocked in the meantime, I can cover it on contract: {{service_pitch}}. Fixed price, no notice period — it won''t get in the way of your hiring.

{{hypothesis}} Two things would tell me if I can help: what''s most urgent right now, and when do you need it live?

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won''t write again.'
WHERE segment = 'hiring' AND channel = 'email' AND display_only = false;

UPDATE template
SET body = 'Hi {{first_name}},

I saw you''re hiring a {{role}} — {{observation}}.

Filling that seat usually takes a couple of months. If something''s blocked in the meantime, I can cover it on contract: {{service_pitch}}. Fixed price, no notice period — it won''t get in the way of your hiring.

{{hypothesis}} Two things would tell me if I can help: what''s most urgent right now, and when do you need it live?

Anthony · +254 700 536 326 · github.com/anthonyalando8
{{mailing_address}}

This is a one-time message about contract work. Reply "stop" and I won''t contact you again.'
WHERE segment = 'hiring_us' AND channel = 'email' AND display_only = false;

UPDATE template
SET body = 'Hi {{first_name}},

{{observation}}.

I''m a backend engineer — Go, Postgres, M-Pesa. I just finished the payments and wallet architecture for a mobility platform, so this is fresh: STK push and verified callbacks, built so nobody gets double-charged and everything reconciles on its own.

{{hypothesis}} If getting payments working is on your critical path, I can do it in 7 days for a fixed KSh 55,000, half upfront. Want a quick call to see if it fits?

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won''t write again.'
WHERE segment = 'incubator' AND channel = 'email' AND subject = 'Payments on {{company}}';

UPDATE template
SET body = 'Hi {{first_name}},

I saw {{company}} is hiring for {{role}} — {{observation}}.

I''m not trying to compete with that hire. I work on one narrow problem some backend teams still find painful even with a full team in place: M-Pesa reconciliation — making sure STK push callbacks match the right transaction reliably, even at volume, without anyone getting double-charged. I built this recently for a mobility platform''s payments stack.

{{hypothesis}} If that specific piece is ever a headache on your side, happy to compare notes — no pitch, just an outside view from someone who''s solved it before.

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won''t write again.'
WHERE segment = 'enterprise' AND channel = 'email' AND display_only = false;
