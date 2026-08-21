-- A send_log row means "SMTP accepted the handoff", not "delivery succeeded" — Gmail (and most
-- providers) reject a genuinely dead mailbox asynchronously, as a bounce notification minutes or
-- hours later, not during the SMTP conversation itself. This lets a human record that after the
-- fact, with a reason, and excludes a bounced send from the 30-day same-domain dedupe check
-- (HasRecentSendToDomain) — a bounce is evidence nothing was actually delivered, so it must not
-- block a corrected resend the way a real successful send would.
ALTER TABLE send_log ADD COLUMN bounced_at timestamptz;
ALTER TABLE send_log ADD COLUMN bounce_reason text;
