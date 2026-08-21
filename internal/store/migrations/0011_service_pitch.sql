-- Both the agency and hiring templates hard-coded the same capability line into every email
-- regardless of company — always leading with M-Pesa/payments even when nothing about the
-- specific company suggests that's the fit (a company hiring a generic "Backend Developer" with
-- no payments angle got the identical pitch as one hiring for an explicitly payments/payroll
-- role). {{service_pitch}} is now filled deterministically by internal/draft/generate.go's
-- servicePitch() based on real signals (paybill_listed/payment_complaint/checkout_present, a
-- hiring_backend job ad that's explicitly about payments/payroll, or a frontend-only/
-- offers_tech_services signal suggesting a backend gap instead) — not by the model.
UPDATE template
SET body = 'Hi {{first_name}},

I came across {{company}} — {{observation}}.

I''m a backend engineer in Nairobi — Go, Postgres, M-Pesa — and I take on subcontract work for agencies: you keep the client relationship, I deliver the server side. Fixed price, one to three weeks per job. Where I come in: {{service_pitch}}.

{{hypothesis}} Worth a 15-minute call if something like that comes up?

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won''t write again.'
WHERE segment = 'agency' AND channel = 'email' AND display_only = false;

UPDATE template
SET body = 'Hi {{first_name}},

I saw you''re hiring a {{role}} — {{observation}}.

Filling that seat usually takes a couple of months. If something is blocked in the meantime, I can work on contract: {{service_pitch}}. Fixed price per scope, no notice period, and it doesn''t interfere with your hiring.

{{hypothesis}} Two questions and I''ll know if I can help: what''s the most urgent thing waiting on that hire, and when did you need it live?

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won''t write again.'
WHERE segment = 'hiring' AND channel = 'email' AND display_only = false;
