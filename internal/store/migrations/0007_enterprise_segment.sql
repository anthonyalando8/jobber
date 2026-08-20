-- A bank, insurer, or other large institution hiring a backend role isn't a good fit for the
-- "hiring" segment's pitch ("contract cover while you hire") — they have their own HR/hiring
-- apparatus and that offer reads as naive. internal/score now classifies these into a distinct
-- "enterprise" segment (same priority as "hiring", not deprioritized — see scorer.go); this is
-- its template: a narrower, consultant-style angle that doesn't compete with their hiring
-- process.
INSERT INTO template (segment, channel, subject, body, display_only) VALUES
('enterprise', 'email', 'Not about your {{role}} role — a narrower question',
'Hi {{first_name}},

I saw {{company}} is hiring for {{role}} — {{observation}}.

I''m not trying to compete with that hire. I work on one narrow problem some backend teams still find painful even with a full team in place: M-Pesa reconciliation — matching STK push callbacks to the right transaction reliably, at volume, with no double charges. I built this recently for a mobility platform''s payments stack.

{{hypothesis}} If that specific piece is ever a headache on your side, happy to compare notes — no pitch, just an outside view from someone who''s solved it before.

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won''t write again.',
false);
