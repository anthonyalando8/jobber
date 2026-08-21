-- Tightens the agency/email template: a real sent draft opened by pointing out the recipient's
-- own site hadn't been updated since 2022 and ran WordPress — sent to a company that itself
-- sells web design/software development as a service. Paired with the signal-filtering fix in
-- internal/draft/generate.go (relevantSignalsForSegment now strips a company's own tech-stack/
-- staleness signals for the "agency" segment unconditionally), this rewrite also makes the
-- capability pitch itself more direct and listy, per direct feedback that the value prop should
-- read as "here's what I can do for you", not soften into a vague "I'd like to be the person you
-- call."
UPDATE template
SET body = 'Hi {{first_name}},

I came across {{company}} — {{observation}}.

I''m a backend engineer in Nairobi — Go, Postgres, M-Pesa — and I take on subcontract work for agencies: you keep the client relationship, I deliver the server side. Fixed price, one to three weeks per job. Where I come in: payments and M-Pesa integrations done properly, an API your team can build a frontend against, or a backend piece picked up mid-project.

{{hypothesis}} Worth a 15-minute call if something like that comes up?

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won''t write again.'
WHERE segment = 'agency' AND channel = 'email' AND display_only = false;
