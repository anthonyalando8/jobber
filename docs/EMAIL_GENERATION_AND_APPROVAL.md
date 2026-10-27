# Email generation, AI review and manual approval

Status: **implemented and verified locally; not deployed.** The tracked schema is now **30**
(`0030_manual_approval.sql`, additive). No email, draft, review result, approval, conversation or
send record was deleted, overwritten or regenerated. Nothing was sent.

## 1. Why the generator wrote defensive emails

The generator was told to. `prompts/specific_job_application.txt` said *"Acknowledge a relevant
gap honestly when needed"*. The snapshot it writes from also includes the job's unmet requirements
(`match.gaps`) and the job text, and the job text asks for Node.js.

Three rules in the system disagreed:

| Rule | What it said about "Go and Python rather than Node.js" |
| --- | --- |
| Generator prompt | Acknowledge the gap. |
| AI reviewer prompt | An honest gap is not a claim of experience; it is fine. |
| Deterministic validator | Naming any technology outside the plan is a **critical** block, even as a gap. Its gap detector also missed "rather than". |

So the generator kept writing a sentence that the validator was certain to reject.

## 2. Why the corrected email was rejected

Three things happened. Only the first was a real problem with the email.

1. **`fabricated_attachment` (critical, correct).** *"My CV is attached for your review."* Emails
   in this system carry no attachments, so the recipient would have received no CV. The reviewer
   was right. The fix is to offer the CV ("I can share my CV on request") or link it from your
   profile.
2. **`application_intent_unclear` (warning, rule too narrow).** The check looked only for the
   words *apply*, *applying* or *application*. *"I am writing to express my strong interest in the
   Senior Backend Engineer role"* states the purpose just as clearly. The check now also accepts
   interest in the exact advertised title.
3. **`review_unavailable` / `http_503` (not a verdict).** Gemini was overloaded and returned
   HTTP 503, so the AI review never ran. Nothing was judged.

A regression test (`TestDuckDuckGoCorrectedEmailFailsOnlyOnTheAttachmentClaim`) runs your
corrected email word for word. It confirms that `fabricated_attachment` is now the only failure,
and that replacing the attachment sentence makes it pass.

## 3. What changed

### Generation (`internal/composition`)

**The prompts now favour strengths.** The writer must never volunteer a missing skill,
technology, qualification or years of experience, apologise, or hedge. It may name only
`approved_technologies`. Each technology in `do_not_mention` is forbidden in any form, including
as a contrast. "Attached" is forbidden. A CV may be mentioned only when your profile includes one.
The business-outreach prompt now says outright that it is about the prospect's business and a
service, never a job request.

**The writer is told the validator's rules in advance.** Each request carries
`approved_technologies` and `do_not_mention`. Both lists are computed by the same function the
validator uses (`UnapprovedTechnologies`), so writer and validator cannot disagree about what is
allowed.

**Validation runs before the email is shown.** If a generated draft still contains a sentence that
only points out a missing technology, that sentence is removed. Removing it cannot make the email
less true. The removal is recorded as a `removed_gap_sentence` note and shown on the page. A
sentence that *claims* a technology you do not have is never removed silently: it goes back to the
writer for repair, or to you.

**A critical failure skips the AI review.** If a draft fails a critical deterministic check, the
reviewer cannot override that, so it is not called. The engine repairs immediately, still within
the existing limit of two repairs. Once that limit is reached, it stops and shows the findings.
There is no loop. A typical defective draft now costs one write and one review, not three writes
and three reviews.

**A person's edit is never rewritten.** Your text is validated and reviewed exactly as you wrote
it.

### Review

**The reviewer uses three explicit levels.**

| Level | Meaning |
| --- | --- |
| **Critical** (`block`) | The email would mislead: invented experience, technology or project claims, the wrong company, title or recipient, a false attachment, an unsupported number. |
| **Warning** (`review`) | True, but a real weakness: unclear relevance, loosely supported claims, a volunteered weakness, generic content that could go to anyone. |
| **Suggestion** (`info`) | Tone, structure, wording, opening style, length, the wording of the call to action. A suggestion never fails the review. |

The reviewer must quote the excerpt and say what to change. It may not raise a finding only
because the email could be written differently.

**Deterministic checks use the same levels.** Word-count guidance is now a suggestion. Stock AI
phrases ("unique blend", "dynamic team") stay a warning. Saying you are interested in the exact
advertised role now counts as a clear application.

**The page shows the levels.** The *What's stopping this email* card labels each finding *Must
fix* (a factual check), *Warning*, *AI: critical* or *AI: warning*, and explains when the AI review
was skipped or failed.

### Prompt changes no longer lock existing drafts

Before this change, any edit to a prompt marked every existing draft "stale". A stale draft could
not be edited, approved or sent. That would have locked your current drafts on this deploy.

A prompt or model change is now recorded separately (`ProviderChanges`):

- Editing is allowed, because saving an edit is how a draft reaches the current reviewer.
- The AI "passed" verdict counts only if it came from the current reviewer.
- Automatic sending treats these changes as stale, exactly as before.

Real data changes still block editing, approval and sending, as before: a changed plan, profile,
job or recipient.

## 4. How generation and review are aligned

Both read the same pinned snapshot:

- profile revision and its facts;
- plan selection;
- approved technologies, which come from the selected capabilities and projects;
- resources;
- job title and match;
- source excerpts.

On top of that snapshot:

- The **technology rule** is a single function, used both to brief the writer and to validate
  the result.
- The **attachment and CV rule** is identical in the writer prompt, the validator and the
  reviewer prompt.
- The **gap rule** is identical everywhere: the writer must not mention a gap; a gap is a warning
  in the validator and in the reviewer prompt; and a gap in a generated draft is removed.

## 5. How manual approval works

On a draft whose AI review did not pass (status `review_required`), the **Approval** panel shows
**Approve manually**:

1. Read the email and the findings in *What's stopping this email*.
2. Edit and **Save edit and review** if you want to. An edit withdraws any approval: the edit
   handler now revokes it explicitly, instead of relying only on the revision check.
3. Tick *"I have read the AI feedback and approve this exact email to … anyway"*. You can add a
   reason. Then click **Approve manually**.
4. The approval is stored with `basis = manual_override` and your note. The page then shows
   **"Approved manually, despite AI feedback"**.
5. The review status stays `review_required`, and the AI findings stay exactly as they were.
   Nothing is relabelled as an AI pass. The basis cannot be changed afterwards: the database
   trigger refuses it.
6. **Send this approved email now** appears only when every other send check passes. It asks for
   confirmation before sending. Approving never sends.

**What a manual approval does not override.** Approving manually answers the AI's objections and
the warnings. It does not override any of the following:

- Critical deterministic checks: a false attachment, an unconfigured link, a technology outside
  the plan, the wrong title, an invented name, placeholders, an unsupported number. They are
  re-checked from the text under the current rules at approval time and again at send time.
  While any fails, the panel lists them instead of the button.
- An unfinished or failed run.
- Data that changed since the draft was written (stale inputs).
- The approval being bound to the exact revision, content hash and recipient.
- The rollout mode and SMTP readiness.
- Suppression lists.
- Per-domain and daily limits.
- Duplicate-send protection, through the idempotency key and reservation.
- Sign-in.

**Manual approvals are never sent automatically.** Automation already skips any draft with a
human approval.

## 6. How existing emails are preserved

- Migration 0030 only adds `basis` (defaulting to `reviewed`) and `override_note`. Every existing
  approval keeps its meaning and is labelled as approved after review.
- No draft, revision, review result, plan, conversation or send attempt is modified. The new rules
  apply to new writes and new checks.
- Historical drafts are not regenerated. Gap-sentence removal applies only to newly generated
  text, never to stored or edited content.
- Drafts written with the old prompts stay editable and approvable (section 3).

## 7. Tests

`go vet` and `gofmt` are clean. `go test ./...` passes in all 24 packages, against isolated
PostgreSQL databases. The new tests are:

- **Composition (DuckDuckGo regression):**
  - The writer is told Node.js is forbidden.
  - The defensive sentence never reaches the draft, with nothing relevant lost.
  - It costs one write and one review.
  - The corrected email fails only on the attachment claim, and passes once that is fixed.
  - A sentence that *claims* Node.js is held for repair rather than removed, and the reviewer is
    never asked about it.
  - A person's edit is never rewritten.
  - Gap removal keeps paragraphs intact.
- **Dispatch, on the real store and preflight with a fake transport:**
  - A manual approval sends once, despite AI feedback, using the approved bytes.
  - A second send is refused (duplicate protection).
  - The AI verdict and findings are unchanged afterwards.
  - The basis cannot be relabelled.
  - A stale content hash is refused.
  - Manual approval works when the review was unavailable.
  - A false-attachment claim cannot be approved manually, and cannot be sent even if an approval
    is forced into the database.
  - Review-only mode and a newer revision still block sending.
  - A normal approval is recorded as `reviewed`.
  - The existing test that human-approved drafts are never automated still passes.
- **Web:**
  - The manual approval panel appears, or is replaced by the critical findings.
  - The two kinds of approval are labelled differently.
  - The acknowledgement is required before anything else is read or written.
- **Existing tests:**
  - The adversarial suite now asserts the stronger guarantee: a critical finding is held without
    asking the reviewer.
  - The prompt tests still pin every protected instruction.

**Local run against PostgreSQL**, with migration 0030 applied through `backup-verify` and then
`apply -proof`:

- A copy of your corrected email produced exactly one finding, `fabricated_attachment`.
- The fixed revision, with the AI review unavailable (`http_503`), showed **Approve manually**.
- Submitting without the acknowledgement was stopped.
- With it, the approval was stored as `manual_override` with its note, and the status stayed
  `review_required`.
- Send eligibility then listed only this environment's own blockers (no SMTP, review-only mode).

## 8. Remaining limitations

- **No live model was called.** The prompt changes are verified as text and through the
  deterministic pipeline, not against Gemini's actual output. Expect fewer defensive sentences.
  The removal step and the validator catch any that remain.
- **Gap removal works on whole sentences.** When a sentence mixes a gap with a positive point
  ("…rather than Node.js, my experience … aligns…"), the positive half goes too. That is
  deliberate: rewriting it would need the writer, so the result is shorter but always true.
- **Technologies outside the vocabulary are not checked.** The vocabulary covers about 40 common
  technologies; names outside it (Gin, Celery, pgx) are not checked deterministically and rely on
  the reviewer.
- **The AI reviewer still says `block` / `review` / `info` internally.** These are the existing
  stored codes, now documented as critical / warning / suggestion. No stored review was rewritten.
- **A Gemini 503 is not retried automatically.** That keeps request accounting simple. Save the
  edit again to retry, or approve manually.

## Deploying

```bash
cd /opt/apps/prospector
git pull --ff-only
bash deploy/deploy.sh      # backs up, verifies the restore, applies 0030, starts the new app
```

For your current DuckDuckGo draft:

1. Replace *"My CV is attached for your review"* with *"I can share my CV on request"*.
2. Click **Save edit and review**.
3. If the review passes, click **Approve**. If it is unavailable again, or raises only warnings
   you are happy with, click **Approve manually**.
4. Click **Send**.
