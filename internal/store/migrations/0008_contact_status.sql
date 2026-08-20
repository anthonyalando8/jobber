-- Lets a human mark a contact as dead (a bounce noticed after the fact, an SMTP rejection at
-- send time) so auto-pick (send-time recipient selection, GetBestEmailContact) skips it, without
-- losing the record — enrichment sometimes finds a dead mailbox, and the reason stays visible
-- for whoever looks at the company next.
ALTER TABLE contact ADD COLUMN status text not null default 'active'; -- active|bad
ALTER TABLE contact ADD COLUMN bad_reason text;
ALTER TABLE contact ADD COLUMN bad_at timestamptz;
