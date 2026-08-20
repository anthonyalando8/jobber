package draft

// FollowUp1Template is the +3d follow-up (see OUTREACH_TEMPLATES.md). Literal text — no model
// involvement, only {{first_name}} is filled.
const FollowUp1Template = `Hi {{first_name}} — following up on the note below in case it got buried. One line is enough: is backend/payments work something you'd ever outsource? If not, no problem and I won't chase.`

// FollowUp2Template is the +8d follow-up. Literal text — only {{company}} is filled.
const FollowUp2Template = `Last note from me. I wrote up how to make M-Pesa payments reliable — the reconciliation part most integrations skip. Sending it in case it's useful to whoever handles that for you. Good luck with {{company}}.`

// RenderFollowUp1 fills the +3d follow-up template.
func RenderFollowUp1(firstName string) string {
	return fillVars(FollowUp1Template, map[string]string{"first_name": firstName})
}

// RenderFollowUp2 fills the +8d follow-up template.
func RenderFollowUp2(company string) string {
	return fillVars(FollowUp2Template, map[string]string{"company": company})
}
