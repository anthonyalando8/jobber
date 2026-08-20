-- review.md §31: follow-ups must be explicitly approved and sent, not just displayed for
-- copy-paste. Track when each one actually went out so ListFollowupsDue stops showing a
-- follow-up that's already been sent (previously nothing cleared the "due" state at all).
ALTER TABLE send_log ADD COLUMN followup_1_sent_at timestamptz;
ALTER TABLE send_log ADD COLUMN followup_2_sent_at timestamptz;
