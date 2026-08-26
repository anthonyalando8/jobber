# Outreach templates — data for the Prospector tool

These are the literal message templates the tool fills. The model only ever supplies
`{{observation}}` and `{{hypothesis}}` (see PROSPECTOR_SPEC.md section 7). Everything else
is fixed text. Store these as seed rows in a `template` table or as files in
`internal/draft/templates/` — either is fine.

Sender constants:

```
name    Anthony Alando
email   anthonyalando8@gmail.com
phone   +254 700 536 326
site    antho-tech.netlify.app
github  github.com/anthonyalando8
```

## Rules the code must enforce

1. Every send needs a non-empty `{{observation}}`. No observation, no send.
2. The ask is always a 15-minute call, never "give me work".
3. LinkedIn variants are **display only** — the tool shows them for copy-paste, it never
   sends them.
4. WhatsApp and phone variants are display only too. Mark as sent manually.
5. Refuse to send any body still containing `[` or `{{`.

---

## segment: agency  ·  channel: email

The recipient is itself a technical company — a web/software/app development shop. The
observation must never point at their own site's tech stack, age, or polish as if it were a gap
("your site runs WordPress", "hasn't been updated since 2022") — to a company that professionally
does this work, that reads as irrelevant at best, condescending at worst. See
`relevantSignalsForSegment` in `internal/draft/generate.go`, which strips tech-stack/staleness
signals unconditionally for this segment, and the `offers_tech_services` signal
(`internal/enrich`) that's meant to ground it instead.

Subject: `Backend overflow work — Go / M-Pesa`

```
Hi {{first_name}},

I came across {{company}} — {{observation}}.

I'm a backend engineer in Nairobi (Go, Postgres, M-Pesa), and I take on subcontract work
for agencies. You keep the client relationship, I handle the server side — usually one to
three weeks per job, fixed price. What I'd actually help with: {{service_pitch}}.

{{hypothesis}} Worth a quick 15-minute call if something like that comes up?

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won't write again.
```

`{{service_pitch}}` is filled deterministically by `servicePitch()` in `internal/draft/generate.go`
based on real signals — never by the model. It leads with payments/M-Pesa only when a signal
actually supports it (a paybill number, a checkout, or a hiring_backend job ad that's explicitly
about payments/payroll); leads with the backend/API build-out angle when the company looks
frontend-only (`offers_tech_services`, a `tech:*` signal) instead; otherwise falls back to the
original balanced phrasing. Before this, every agency email defaulted to leading with M-Pesa
regardless of fit — including a real sent draft to a web design company with no payments angle
at all.

## segment: agency · channel: linkedin_dm (display only, <300 chars)

```
Hi {{first_name}} — saw {{observation}}. I'm a Go/Postgres backend dev in Nairobi doing
subcontract work for agencies: M-Pesa integrations and APIs for mobile teams, fixed price
per scope. Do you ever outsource backend work? Happy to send a one-pager.
```

---

## segment: incubator · channel: email (programme / innovation manager)

Subject: `Backend support for cohort teams`

```
Hi {{first_name}},

I'm Anthony, a backend engineer in Nairobi — {{observation}}.

Most early teams I meet hit the same wall: M-Pesa payments and a backend for their app,
with no server-side engineer on the team. That's exactly what I do, on short fixed-price
scopes that fit a startup budget.

Would you be open to pointing me at two or three teams who are stuck there? Happy to do a
free 30-minute review for any of them so they can judge the value first.

Anthony
+254 700 536 326 · anthonyalando8@gmail.com

Tell me to stop and I won't write again.
```

## segment: incubator · channel: email (founder direct)

Subject: `Payments on {{company}}`

```
Hi {{first_name}},

{{observation}}.

I'm a backend engineer — Go, Postgres, M-Pesa. I just finished the payments and wallet
architecture for a mobility platform, so this is fresh: STK push and verified callbacks,
built so nobody gets double-charged and everything reconciles on its own.

{{hypothesis}} If getting payments working is on your critical path, I can do it in 7 days
for a fixed KSh 55,000, half upfront. Want a quick call to see if it fits?

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won't write again.
```

---

## segment: hiring · channel: email

Subject: `Your {{role}} role — contract cover while you hire`

```
Hi {{first_name}},

I saw you're hiring a {{role}} — {{observation}}.

Filling that seat usually takes a couple of months. If something's blocked in the
meantime, I can cover it on contract: {{service_pitch}}. Fixed price, no notice period —
it won't get in the way of your hiring.

{{hypothesis}} Two things would tell me if I can help: what's most urgent right now, and
when do you need it live?

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won't write again.
```

`{{service_pitch}}` — same deterministic fill as the agency template above. A real example: a
"Checkoff Product Scheme Manager" job ad (a payroll-deduction role) had no site-level payment
signal at all, but the role text itself is a far stronger, more specific cue than any generic
site signal — `servicePitch()` catches it via `paymentRoleKeywords` and leads with M-Pesa/payments
regardless. A generic "Backend Developer" ad with no payments angle instead gets the neutral
fallback, not the same M-Pesa-first pitch as a genuinely payments-relevant role.

---

## segment: hiring_us · channel: email

Same shape as `hiring` above, sourced from RemoteOK (US/international remote postings) instead of
a Kenyan job board. Two differences, both required rather than stylistic:

- `{{service_pitch}}` is computed with `market="us"`, which drops M-Pesa branding entirely
  (meaningless to that audience) in favor of generic "payment integrations" wording.
- The body includes `{{mailing_address}}` and an explicit opt-out line — CAN-SPAM requires a
  valid physical postal address and a working opt-out mechanism in every commercial email sent to
  a US recipient, neither of which the Kenya templates needed. `internal/draft/generate.go`
  refuses to draft this segment at all (skips straight to `needs_manual`, no AI call spent) until
  `US_MAILING_ADDRESS` is configured — see `.env.example`.

Subject: `Your {{role}} role — contract cover while you hire`

```
Hi {{first_name}},

I saw you're hiring a {{role}} — {{observation}}.

Filling that seat usually takes a couple of months. If something's blocked in the
meantime, I can cover it on contract: {{service_pitch}}. Fixed price, no notice period —
it won't get in the way of your hiring.

{{hypothesis}} Two things would tell me if I can help: what's most urgent right now, and
when do you need it live?

Anthony · +254 700 536 326 · github.com/anthonyalando8
{{mailing_address}}

This is a one-time message about contract work. Reply "stop" and I won't contact you again.
```

Note for the code: `{{role}}` comes from the job-ad signal, not from the model.

---

## segment: enterprise · channel: email

A bank/insurer/university/hospital/government-scale institution hiring the role in
`{{role}}` — `isLikelyEnterprise` (`internal/score/scorer.go`) routes these here instead of
`hiring`, since the ordinary "contract cover while you hire" pitch reads oddly at that scale:
a company with its own HR department and procurement process doesn't need help filling one
seat. Narrower angle instead — one specific technical problem (M-Pesa reconciliation),
positioned as a comparison-of-notes, not a pitch to replace the hire.

Subject: `Not about your {{role}} role — a narrower question`

```
Hi {{first_name}},

I saw {{company}} is hiring for {{role}} — {{observation}}.

I'm not trying to compete with that hire. I work on one narrow problem some backend teams
still find painful even with a full team in place: M-Pesa reconciliation — making sure STK
push callbacks match the right transaction reliably, even at volume, without anyone getting
double-charged. I built this recently for a mobility platform's payments stack.

{{hypothesis}} If that specific piece is ever a headache on your side, happy to compare
notes — no pitch, just an outside view from someone who's solved it before.

Anthony · +254 700 536 326 · github.com/anthonyalando8

Tell me to stop and I won't write again.
```

---

## segment: sme · channel: email or whatsapp

No technical words in this template. No "API", no "integration", no "backend".

Subject: `Automating your payment confirmations`

```
Hello {{first_name}},

I noticed {{observation}}.

I build small systems that remove that daily work: payments confirmed automatically and
matched to the right customer, receipts sent on their own, and one simple report at the end
of the day. It takes about a week and costs a fixed amount agreed before we start.

Would a quick call tomorrow be useful? I'll tell you honestly whether it's worth doing for
a business your size.

Anthony Alando · +254 700 536 326

Tell me to stop and I won't write again.
```

## segment: sme · channel: phone_script (display only)

```
"Good morning, is this {{first_name}}? My name is Anthony, I build business systems here in
Nairobi — I'm not selling anything on this call. I noticed {{observation}}. Can I ask, who
currently checks that payments have come in, and how long does that take each day?"

Then listen. The number they say is the quote.
```

---

## Follow-ups (all segments)

`followup_1`, due +3 days:

```
Hi {{first_name}} — following up on the note below in case it got buried. One line is
enough: is backend/payments work something you'd ever outsource? If not, no problem and I
won't chase.
```

`followup_2`, due +8 days — give something instead of asking:

```
Last note from me. I wrote up how to make M-Pesa payments reliable — the reconciliation
part most integrations skip. Sending it in case it's useful to whoever handles that for
you. Good luck with {{company}}.
```

`referral_ask` — send after any reply, including a no:

```
Understood, thanks for replying. One favour — is there anyone you know who's stuck on
payments or a backend right now? A name is all I need.
```

---

## Objection handling (for the human, shown on the review screen sidebar)

| They say | Response |
| --- | --- |
| "Come work for us, we pay end month." | "I'm not available for employment — I'm in a role already. I take project work at a fixed price, half upfront and half on handover. Shall I scope the payments piece on that basis?" |
| "Can we pay after delivery / net-30?" | "I work half upfront — it's how I hold the delivery date for you. If cash flow is the issue, I can split it into two smaller scopes so you pay as each piece lands." |
| "Can you do it hourly?" | "Fixed price protects you more than hourly — you know the number before we start, and overruns are my problem." |
| "Too expensive." | "Understood. What's the smallest version that would still be worth it to you?" Cut scope, never price. |
| "Do a free test task first." | "I'll do a free 30-minute review and tell you exactly what's wrong — that's the sample. Build work starts on deposit." |
