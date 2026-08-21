-- New segment for RemoteOK-sourced (US/international remote) hiring leads — same shape as the
-- "hiring" template but never mentions M-Pesa (meaningless to that audience; see servicePitch's
-- market parameter in internal/draft/generate.go) and adds the two things CAN-SPAM requires that
-- the Kenya templates never needed: a physical postal address ({{mailing_address}}, filled from
-- US_MAILING_ADDRESS — internal/draft/generate.go refuses to draft at all for this segment until
-- that's configured) and an explicit, unambiguous opt-out line.
INSERT INTO template (segment, channel, subject, body, display_only) VALUES
('hiring_us', 'email', 'Your {{role}} role — contract cover while you hire',
'Hi {{first_name}},

I saw you''re hiring a {{role}} — {{observation}}.

Filling that seat usually takes a couple of months. If something is blocked in the meantime, I can work on contract: {{service_pitch}}. Fixed price per scope, no notice period, and it doesn''t interfere with your hiring.

{{hypothesis}} Two questions and I''ll know if I can help: what''s the most urgent thing waiting on that hire, and when did you need it live?

Anthony · +254 700 536 326 · github.com/anthonyalando8
{{mailing_address}}

This is a one-time message about contract work. Reply "stop" and I won''t contact you again.',
false);
