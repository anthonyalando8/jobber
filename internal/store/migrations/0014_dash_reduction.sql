-- Second pass at "this reads AI-written": the tone pass in 0013 fixed comma-spliced feature
-- lists, but left several em dashes inside the fixed template prose itself (as opposed to the
-- one em dash every template structurally needs to introduce {{observation}} — that one stays,
-- it's load-bearing, not a style tell). A real email from a person leans on periods and commas,
-- not a string of "—" doing the work of several different kinds of pause. Swapped the removable
-- ones for the punctuation an actual sentence would use.

UPDATE template
SET body = 'Hi {{first_name}},

I came across {{company}} — {{observation}}.

I''m a backend engineer in Nairobi (Go, Postgres, M-Pesa), and I take on subcontract work for agencies. You keep the client relationship, I handle the server side, usually one to three weeks per job, fixed price. What I''d actually help with: {{service_pitch}}.

{{hypothesis}} Worth a quick 15-minute call if something like that comes up?

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won''t write again.'
WHERE segment = 'agency' AND channel = 'email' AND display_only = false;

UPDATE template
SET body = 'Hi {{first_name}},

I saw you''re hiring a {{role}} — {{observation}}.

Filling that seat usually takes a couple of months. If something''s blocked in the meantime, I can cover it on contract: {{service_pitch}}. Fixed price, no notice period, and it won''t get in the way of your hiring.

{{hypothesis}} Two things would tell me if I can help: what''s most urgent right now, and when do you need it live?

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won''t write again.'
WHERE segment = 'hiring' AND channel = 'email' AND display_only = false;

UPDATE template
SET body = 'Hi {{first_name}},

I saw you''re hiring a {{role}} — {{observation}}.

Filling that seat usually takes a couple of months. If something''s blocked in the meantime, I can cover it on contract: {{service_pitch}}. Fixed price, no notice period, and it won''t get in the way of your hiring.

{{hypothesis}} Two things would tell me if I can help: what''s most urgent right now, and when do you need it live?

Anthony · +254 700 536 326 · github.com/anthonyalando8
{{mailing_address}}

This is a one-time message about contract work. Reply "stop" and I won''t contact you again.'
WHERE segment = 'hiring_us' AND channel = 'email' AND display_only = false;

UPDATE template
SET body = 'Hi {{first_name}},

{{observation}}.

I''m a backend engineer: Go, Postgres, M-Pesa. I just finished the payments and wallet architecture for a mobility platform, so this is fresh: STK push and verified callbacks, built so nobody gets double-charged and everything reconciles on its own.

{{hypothesis}} If getting payments working is on your critical path, I can do it in 7 days for a fixed KSh 55,000, half upfront. Want a quick call to see if it fits?

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won''t write again.'
WHERE segment = 'incubator' AND channel = 'email' AND subject = 'Payments on {{company}}';

UPDATE template
SET body = 'Hi {{first_name}},

I saw {{company}} is hiring for {{role}} — {{observation}}.

I''m not trying to compete with that hire. I work on one narrow problem some backend teams still find painful even with a full team in place: M-Pesa reconciliation, making sure STK push callbacks match the right transaction reliably, even at volume, without anyone getting double-charged. I built this recently for a mobility platform''s payments stack.

{{hypothesis}} If that specific piece is ever a headache on your side, happy to compare notes, no pitch, just an outside view from someone who''s solved it before.

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won''t write again.'
WHERE segment = 'enterprise' AND channel = 'email' AND display_only = false;
