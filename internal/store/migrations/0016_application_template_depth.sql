-- The application segment's first draft (0015) read as thin for a real cover letter — one
-- generic sentence about experience, then straight to "happy to send my CV." Expanded to two
-- concrete sentences (still only facts already used and approved elsewhere in these templates:
-- Go/Postgres, the M-Pesa reconciliation pipeline for a mobility platform) plus a line on how he
-- works, so it reads as a fuller application rather than a one-line cold pitch reusing a
-- cover-letter subject line.
UPDATE template
SET body = 'Hi {{first_name}},

I''d like to apply for the {{role}} role at {{company}} — {{observation}}.

I''m a backend engineer in Nairobi (Go, Postgres, distributed payment systems). Over the last few years I''ve built and shipped production systems end to end, including a full M-Pesa reconciliation pipeline for a mobility platform''s payments stack: STK push callbacks matched to the right transaction reliably, at volume, with no double charges. I''m comfortable owning a service from design through deployment, and I pick up unfamiliar codebases quickly.

I''d welcome the chance to bring that experience to this role. Happy to send my CV, references, or walk through specific projects in more depth.

Anthony Alando · +254 700 536 326 · github.com/anthonyalando8
{{mailing_address}}

This is a one-time message about my interest in this role. Reply "stop" and I won''t reach out again.'
WHERE segment = 'application' AND channel = 'email' AND display_only = false;
