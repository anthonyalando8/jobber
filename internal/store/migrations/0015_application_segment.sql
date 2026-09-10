-- New "application" segment: a genuine job-application email instead of the "let me cover this
-- on contract while you hire" pitch every other hiring-style template makes. For a company large
-- enough that a contract-cover pitch reads as presumptuous (a company.html segment override, set
-- by a human judgment call — see classifySegment in internal/score/scorer.go, which treats
-- "application" as sticky and never assigns it automatically), this applies as an actual
-- candidate instead. Same {{role}}/{{observation}} grounding as hiring/hiring_us — reuses the
-- same AI observation pipeline and CAN-SPAM mailing-address guard (now keyed off the company's
-- source, not its segment, so a US-sourced company switched into "application" mode still gets
-- the guard) — just different fixed copy around it. No {{service_pitch}}/{{hypothesis}}: those
-- are written as a contract-work pitch and don't fit an application.
INSERT INTO template (segment, channel, subject, body, display_only) VALUES
('application', 'email', 'Application: {{role}} at {{company}}',
'Hi {{first_name}},

I''d like to apply for the {{role}} role at {{company}} — {{observation}}.

I''m a backend engineer in Nairobi (Go, Postgres, distributed payment systems), and I''ve built and shipped production systems end to end, including a full M-Pesa reconciliation pipeline for a mobility platform''s payments stack. I''d welcome the chance to bring that experience to this role.

Happy to send my CV or walk through specific projects if that''s useful.

Anthony Alando · +254 700 536 326 · github.com/anthonyalando8
{{mailing_address}}

This is a one-time message about my interest in this role. Reply "stop" and I won''t reach out again.',
false);
