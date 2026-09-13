# Design: resolving unresolved send attempts

Status: **design only, not implemented.** Phase 7 deliberately did not change the Phase 5
send-state invariants. This document exists so the change can be reviewed on its own terms, and
so the operator recovery procedure is written down in the meantime.

## The problem

Three attempt states are unresolved — the system does not know what it has done:

| State | Meaning | How it happens |
| --- | --- | --- |
| `reserved` | A row exists; the provider was never contacted. | Crash between `ReserveSend` and `MarkSending`. |
| `sending` | Transmission was in flight. | Crash between `MarkSending` and `SettleSend`. |
| `uncertain` | The terminating dot was written and no verdict came back. | Network failure at the SMTP commit point. |

All three are blocking states: they hold the idempotency key, so no further send of that logical
message is possible. That is the correct default — it fails towards *not sending twice* — but
there is currently no way out of it except leaving the draft permanently stuck.

Automation halts entirely whenever any unresolved attempt exists
(`automation.Evaluate` → `unresolved_send_attempt`), so this is now also a prerequisite for
Phase 7 actually running.

## Why it cannot simply be fixed

`send_attempt` is the audit trail. The `protect_send_attempt` trigger refuses to change the state
of a settled attempt, and `send_attempt_live_key` — the partial unique index that makes
double-transmission impossible — includes `uncertain` in its predicate. So "release the key"
means either changing a settled row's state or changing the index predicate. Both weaken
guarantees that were reviewed and approved in Phase 5, which is why neither was done quietly.

## Proposed design

Add a **resolution record** rather than mutating the attempt. The attempt row stays exactly as
written; what changes is that a separate, attributed statement says what was established, and the
uniqueness predicate learns to respect it.

### Schema (migration 0026, additive)

```sql
CREATE TABLE send_reconciliation (
 id bigserial PRIMARY KEY,
 send_attempt_id bigint NOT NULL UNIQUE REFERENCES send_attempt(id) ON DELETE RESTRICT,
 -- What a person established, and how.
 conclusion text NOT NULL CHECK (conclusion IN ('delivered','not_delivered','still_unknown')),
 basis text NOT NULL CHECK (basis <> ''),
 resolved_by text NOT NULL CHECK (resolved_by <> ''),
 resolved_at timestamptz NOT NULL DEFAULT now()
);
```

Plus a generated marker on `send_attempt` so the index can see it without a join:

```sql
ALTER TABLE send_attempt ADD COLUMN reconciled_not_delivered boolean NOT NULL DEFAULT false;
DROP INDEX send_attempt_live_key;
CREATE UNIQUE INDEX send_attempt_live_key ON send_attempt(idempotency_key)
 WHERE state IN ('reserved','sending','accepted','uncertain','rejected')
   AND NOT reconciled_not_delivered;
```

`reconciled_not_delivered` is the single, narrow permitted mutation, set only by recording a
`not_delivered` reconciliation, and the trigger must refuse to unset it.

### Behaviour by conclusion

| Conclusion | Effect |
| --- | --- |
| `delivered` | Open the conversation thread as though the attempt had been accepted, recording that it was established after the fact rather than reported by the provider. The key stays held: the message went out, so it must never go again. |
| `not_delivered` | Set `reconciled_not_delivered`, releasing the key so a deliberate new send is possible. The attempt row itself still reads `uncertain` for ever — because that is what actually happened. |
| `still_unknown` | Record the investigation and change nothing. The message stays blocked, which is correct. |

### What stays true

- No attempt's `state`, `subject`, `body`, `recipient`, `approval` or timestamps are ever
  rewritten. The audit trail of what was attempted is unchanged.
- Nothing automatic writes a reconciliation. It requires a person and a stated basis, exactly
  like the Phase 6 legacy classification.
- Releasing a key never re-sends anything: it only makes a *new*, separately approved send
  possible.
- Automation remains halted while any attempt is unresolved, including `still_unknown`.

### Why a generated marker rather than a new state

Adding `reconciled_not_delivered` as a *state* would mean an attempt that reads as something
other than what happened at the time. Keeping it a separate boolean preserves the honest reading
— "this attempt ended uncertain, and later a person established the message never arrived" — at
the cost of one column.

## Operator recovery procedure until this exists

There is currently no in-product way to clear an unresolved attempt. If one occurs:

1. **Look at the dashboard.** The v2 panel shows the count under "need reconciliation", and the
   composition page shows the attempt with its stage and error category.
2. **Read the stage.** Anything before `data_terminate` means the message definitely did not go
   out. Only `data_terminate` is genuinely ambiguous.
3. **Establish what happened from the recipient's side** — the sending account's Sent folder or
   the provider's own logs, not from anything in this system.
4. **If it was delivered:** do nothing. The block is correct; the message arrived. Record a
   thread note if a conversation exists.
5. **If it was not delivered:** the draft cannot currently be re-sent under the same key. Produce
   a new composition revision from the contact plan — a new revision has a new content hash and
   therefore a new idempotency key — and approve and send that. This is deliberate friction, not
   a workaround: the new message is reviewed and approved on its own terms.
6. **Do not** delete or edit rows in `send_attempt` to unblock a draft. The triggers refuse, and
   working around them would destroy the record that makes any of this auditable.

Until the design above is implemented, step 5 is the supported route, and automation stays off
for as long as an unresolved attempt exists.
