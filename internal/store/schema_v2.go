package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func checkV2Schema(ctx context.Context, conn *pgx.Conn, version int, report *SchemaReport) error {
	specs := map[string]string{}
	if version >= 17 {
		specs["candidate_profile"] = "id:bigint active_revision_id:bigint created_at:timestamp_with_time_zone"
		specs["candidate_profile_revision"] = "id:bigint profile_id:bigint revision:integer document:jsonb content_hash:text status:text approved_at:timestamp_with_time_zone created_at:timestamp_with_time_zone"
	}
	if version >= 18 {
		specs["opportunity"] = "id:bigint company_id:bigint source:text source_key:text external_id:text url:text canonical_url:text title:text normalized_title:text description:text location:text employment_type:text remote_status:text published_at:timestamp_with_time_zone expires_at:timestamp_with_time_zone availability_status:text description_status:text selection_status:text current_revision:integer content_hash:text metadata:jsonb discovered_at:timestamp_with_time_zone last_seen_at:timestamp_with_time_zone updated_at:timestamp_with_time_zone"
		specs["opportunity_revision"] = "opportunity_id:bigint revision:integer document:jsonb content_hash:text captured_at:timestamp_with_time_zone"
		specs["ingestion_review"] = "id:bigint diagnostic_key:text source:text reason:text discovery:jsonb observations:integer first_seen_at:timestamp_with_time_zone last_seen_at:timestamp_with_time_zone resolved_at:timestamp_with_time_zone"
		specs["legacy_opportunity_backfill"] = "signal_id:bigint opportunity_id:bigint opportunity_revision:integer status:text reason:text processed_at:timestamp_with_time_zone"
	}
	if version >= 19 {
		specs["signal"] = "id:bigint company_id:bigint kind:text detail:text url:text created_at:timestamp_with_time_zone fact_key:text fact_group:text excerpt:text extraction_method:text confidence:text first_seen_at:timestamp_with_time_zone last_seen_at:timestamp_with_time_zone observations:integer revision:integer"
		specs["company"] = "id:bigint name:text domain:text segment:text score:integer business_segment:text"
	}
	if version >= 20 {
		specs["opportunity_match"] = "id:bigint opportunity_id:bigint opportunity_revision:integer profile_revision_id:bigint matcher_version:text classification:text score:integer family_confidence:text result:jsonb assessed_at:timestamp_with_time_zone"
		specs["company_relevance"] = "company_id:bigint profile_revision_id:bigint assessor_version:text score:integer relevant:boolean payment_need_evidenced:boolean result:jsonb assessed_at:timestamp_with_time_zone"
	}
	if version >= 21 {
		specs["contact_plan"] = "id:bigint company_id:bigint intent:text opportunity_id:bigint opportunity_revision:integer provenance_opportunity_id:bigint match_classification:text target_families:jsonb recipient_contact_id:bigint recipient_email:text recipient_name:text recipient_relevance:text recipient_reason:text profile_revision_id:bigint plan_key:text revision:integer selection_origin:text status:text requires_review:boolean reasons:jsonb selection:jsonb created_at:timestamp_with_time_zone updated_at:timestamp_with_time_zone"
		specs["contact_plan_revision"] = "plan_id:bigint revision:integer snapshot:jsonb created_at:timestamp_with_time_zone"
	}
	if version >= 22 {
		specs["draft_composition"] = "draft_id:bigint plan_id:bigint plan_revision:integer profile_revision_id:bigint snapshot:jsonb input_hash:text status:text current_revision:integer lease_token:text lease_until:timestamp_with_time_zone created_at:timestamp_with_time_zone updated_at:timestamp_with_time_zone"
		specs["draft_composition_revision"] = "draft_id:bigint revision:integer content_hash:text attempt:jsonb created_at:timestamp_with_time_zone"
		specs["composition_reservation"] = "draft_id:bigint owner:text remaining:integer expires_at:timestamp_with_time_zone"
		specs["composition_call"] = "id:bigint draft_id:bigint stage:text started_at:timestamp_with_time_zone"
	}
	if version >= 23 {
		specs["send_approval"] = "id:bigint draft_id:bigint composition_revision:integer content_hash:text input_hash:text recipient_contact_id:bigint recipient_email:text intent:text approved_by:text approved_at:timestamp_with_time_zone revoked_at:timestamp_with_time_zone revoked_reason:text"
		specs["outbound_thread"] = "id:bigint company_id:bigint plan_id:bigint plan_revision:integer intent:text recipient_contact_id:bigint recipient_email:text opportunity_id:bigint root_draft_id:bigint root_message_id:text state:text state_reason:text followup_step:integer last_sent_at:timestamp_with_time_zone created_at:timestamp_with_time_zone updated_at:timestamp_with_time_zone"
		specs["send_attempt"] = "id:bigint idempotency_key:text draft_id:bigint composition_revision:integer approval_id:bigint thread_id:bigint company_id:bigint recipient_contact_id:bigint recipient_email:text intent:text kind:text followup_step:integer subject:text body:text content_hash:text message_id:text profile_revision_id:bigint opportunity_id:bigint opportunity_revision:integer plan_id:bigint plan_revision:integer policy_version:text initiator:text state:text provider:text provider_message_id:text provider_response:text provider_stage:text error_category:text reserved_at:timestamp_with_time_zone started_at:timestamp_with_time_zone settled_at:timestamp_with_time_zone"
		specs["thread_event"] = "id:bigint thread_id:bigint send_attempt_id:bigint kind:text detail:text source:text occurred_at:timestamp_with_time_zone"
	}
	if version >= 24 {
		specs["legacy_thread_intent"] = "id:bigint send_log_id:bigint intent:text opportunity_id:bigint recipient_contact_id:bigint target_families:jsonb basis:text classified_by:text classified_at:timestamp_with_time_zone superseded_at:timestamp_with_time_zone"
	}
	if version >= 25 {
		specs["automation_decision"] = "id:bigint draft_id:bigint composition_revision:integer content_hash:text outcome:text policy_version:text intent:text profile_revision_id:bigint opportunity_id:bigint opportunity_revision:integer plan_id:bigint plan_revision:integer recipient_contact_id:bigint recipient_email:text semantic_status:text semantic_hash:text rollout_mode:text allowlist:jsonb daily_cap:integer reasons:jsonb blockers:jsonb send_attempt_id:bigint evaluated_at:timestamp_with_time_zone"
		specs["automation_claim"] = "draft_id:bigint owner:text claimed_at:timestamp_with_time_zone expires_at:timestamp_with_time_zone"
	}
	if version >= 26 {
		specs["contact_discovery_task"] = "company_id:bigint state:text priority:integer eligibility_reason:text intent:text opportunity_id:bigint plan_id:bigint attempts:integer last_searched_at:timestamp_with_time_zone next_eligible_at:timestamp_with_time_zone result_summary:jsonb failure_reason:text claimed_by:text claim_expires_at:timestamp_with_time_zone created_at:timestamp_with_time_zone updated_at:timestamp_with_time_zone"
		specs["contact_candidate"] = "id:bigint company_id:bigint identity_key:text identity_basis:text kind:text person_name:text role_title:text employer_claimed:text email:text email_provenance:text verification:text application_url:text ats_provider:text recipient_function:text function_evidence:text endpoint_purpose:text endpoint_contactable:boolean confidence:text best_source_trust:integer employer_owned_evidence:boolean status:text rejected_reason:text promoted_contact_id:bigint discovered_at:timestamp_with_time_zone updated_at:timestamp_with_time_zone"
		specs["contact_candidate_evidence"] = "id:bigint candidate_id:bigint source_type:text source_trust:integer source_url:text excerpt:text excerpt_hash:text discovery_method:text provider:text employer_owned:boolean observed_at:timestamp_with_time_zone"
	}
	if version >= 27 {
		specs["opportunity_suppression"] = "id:bigint opportunity_id:bigint opportunity_revision:integer reason:text basis:text note:text decided_by:text evidence:jsonb restore_selection_status:text decided_at:timestamp_with_time_zone superseded_at:timestamp_with_time_zone superseded_by:text supersede_note:text"
	}
	if version >= 28 {
		specs["opportunity_access"] = "opportunity_id:bigint opportunity_revision:integer classifier_version:integer eligibility:text kenya_access:text eligibility_evidence:text route:text route_url:text cost:text cost_evidence:text contract:boolean classified_at:timestamp_with_time_zone"
		specs["opportunity_tracking"] = "opportunity_id:bigint state:text note:text applied_at:timestamp_with_time_zone updated_at:timestamp_with_time_zone"
	}
	for name, spec := range specs {
		table, ok := report.Tables[name]
		if !ok {
			return fmt.Errorf("missing v2 table %s", name)
		}
		for _, field := range strings.Fields(spec) {
			parts := strings.SplitN(field, ":", 2)
			found := false
			for _, col := range table.Columns {
				if col.Name == parts[0] && col.Type == strings.ReplaceAll(parts[1], "_", " ") {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("missing/incompatible v2 column %s.%s", name, parts[0])
			}
		}
	}
	triggers := map[string]string{}
	if version >= 23 {
		triggers["send_approval"] = "protect_send_approval"
		triggers["outbound_thread"] = "protect_outbound_thread"
		triggers["send_attempt"] = "protect_send_attempt"
		triggers["thread_event"] = "protect_thread_event"
	}
	if version >= 24 {
		triggers["legacy_thread_intent"] = "protect_legacy_thread_intent"
	}
	if version >= 25 {
		triggers["automation_decision"] = "protect_automation_decision"
	}
	if version >= 22 {
		triggers["draft_composition"] = "immutable_composition_snapshot"
		triggers["draft_composition_revision"] = "immutable_composition_revision"
		triggers["draft"] = "protect_composed_draft"
	}
	if version >= 17 {
		triggers["candidate_profile_revision"] = "immutable_profile_revision"
		triggers["candidate_profile"] = "approved_profile_head"
	}
	if version >= 18 {
		triggers["opportunity_revision"] = "immutable_opportunity_revision"
	}
	if version >= 19 {
		triggers["signal"] = "protect_signal_history"
	}
	if version >= 21 {
		triggers["contact_plan_revision"] = "immutable_contact_plan_revision"
		triggers["contact_plan"] = "protect_contact_plan"
	}
	for table, trigger := range triggers {
		var exists bool
		err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass($1) AND tgname=$2 AND tgenabled IN ('O','A'))`, table, trigger).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("missing/disabled invariant trigger %s", trigger)
		}
	}
	constraints := map[string][]string{}
	if version >= 23 {
		constraints["send_approval"] = []string{"PRIMARY KEY (id)", "FOREIGN KEY (draft_id)",
			"FOREIGN KEY (recipient_contact_id)", "FOREIGN KEY (draft_id, composition_revision)"}
		constraints["outbound_thread"] = []string{"PRIMARY KEY (id)", "FOREIGN KEY (company_id)",
			"FOREIGN KEY (recipient_contact_id)", "FOREIGN KEY (root_draft_id)", "FOREIGN KEY (plan_id, plan_revision)"}
		constraints["send_attempt"] = []string{"PRIMARY KEY (id)", "FOREIGN KEY (approval_id)",
			"FOREIGN KEY (draft_id, composition_revision)", "FOREIGN KEY (plan_id, plan_revision)",
			"FOREIGN KEY (profile_revision_id)", "FOREIGN KEY (recipient_contact_id)"}
		constraints["thread_event"] = []string{"PRIMARY KEY (id)", "FOREIGN KEY (thread_id)"}
	}
	if version >= 24 {
		constraints["legacy_thread_intent"] = []string{"PRIMARY KEY (id)", "FOREIGN KEY (send_log_id)",
			"FOREIGN KEY (opportunity_id)", "FOREIGN KEY (recipient_contact_id)"}
	}
	if version >= 25 {
		constraints["automation_decision"] = []string{"PRIMARY KEY (id)", "FOREIGN KEY (draft_id)",
			"FOREIGN KEY (draft_id, composition_revision)", "FOREIGN KEY (send_attempt_id)",
			"FOREIGN KEY (recipient_contact_id)", "FOREIGN KEY (profile_revision_id)"}
		constraints["automation_claim"] = []string{"PRIMARY KEY (draft_id)", "FOREIGN KEY (draft_id)"}
	}
	if version >= 22 {
		constraints["draft_composition"] = []string{"PRIMARY KEY (draft_id)", "FOREIGN KEY (draft_id)", "FOREIGN KEY (plan_id, plan_revision)", "FOREIGN KEY (profile_revision_id)"}
		constraints["draft_composition_revision"] = []string{"PRIMARY KEY (draft_id, revision)", "FOREIGN KEY (draft_id)"}
		constraints["composition_reservation"] = []string{"PRIMARY KEY (draft_id)", "FOREIGN KEY (draft_id)"}
		constraints["composition_call"] = []string{"PRIMARY KEY (id)", "FOREIGN KEY (draft_id)"}
		if err := requirePartialUniqueIndex(ctx, conn, "draft_composition", "composition_one_running_plan", "(plan_id)", "status"); err != nil {
			return err
		}
	}
	if version >= 17 {
		constraints["candidate_profile"] = []string{"PRIMARY KEY (id)", "FOREIGN KEY (id, active_revision_id)"}
		constraints["candidate_profile_revision"] = []string{"PRIMARY KEY (id)", "UNIQUE (profile_id, revision)", "UNIQUE (profile_id, id)", "FOREIGN KEY (profile_id)"}
	}
	if version >= 18 {
		constraints["opportunity"] = []string{"PRIMARY KEY (id)", "UNIQUE (source, source_key)", "FOREIGN KEY (company_id)", "FOREIGN KEY (id, current_revision)"}
		constraints["opportunity_revision"] = []string{"PRIMARY KEY (opportunity_id, revision)", "FOREIGN KEY (opportunity_id)"}
		constraints["ingestion_review"] = []string{"PRIMARY KEY (id)", "UNIQUE (diagnostic_key)"}
		constraints["legacy_opportunity_backfill"] = []string{"PRIMARY KEY (signal_id)", "FOREIGN KEY (signal_id)", "FOREIGN KEY (opportunity_id, opportunity_revision)"}
	}
	if version >= 20 {
		constraints["opportunity_match"] = []string{"PRIMARY KEY (id)",
			"UNIQUE (opportunity_id, opportunity_revision, profile_revision_id, matcher_version)",
			"FOREIGN KEY (profile_revision_id)", "FOREIGN KEY (opportunity_id, opportunity_revision)"}
		constraints["company_relevance"] = []string{"PRIMARY KEY (company_id)", "FOREIGN KEY (company_id)"}
	}
	if version >= 21 {
		constraints["contact_plan"] = []string{"PRIMARY KEY (id)", "FOREIGN KEY (company_id)",
			"FOREIGN KEY (recipient_contact_id)", "FOREIGN KEY (profile_revision_id)",
			"FOREIGN KEY (opportunity_id, opportunity_revision)"}
		constraints["contact_plan_revision"] = []string{"PRIMARY KEY (plan_id, revision)", "FOREIGN KEY (plan_id)"}
	}
	for table, wanted := range constraints {
		for _, part := range wanted {
			found := false
			for _, actual := range report.Tables[table].Constraints {
				if strings.Contains(actual, part) {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("missing v2 constraint %s: %s", table, part)
			}
		}
	}
	if version >= 18 {
		var valid bool
		err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE i.indrelid='opportunity'::regclass AND c.relname='opportunity_external_id_unique' AND i.indisunique AND i.indisvalid AND pg_get_indexdef(i.indexrelid) LIKE '%(source, external_id)%' AND pg_get_expr(i.indpred,i.indrelid) LIKE '%external_id%<>%')`).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return fmt.Errorf("missing/invalid external opportunity identity index")
		}
	}
	if version >= 19 {
		if err := requirePartialUniqueIndex(ctx, conn, "signal", "signal_canonical_fact",
			"(company_id, fact_key)", "fact_key"); err != nil {
			return err
		}
	}
	if version >= 21 {
		if err := requirePartialUniqueIndex(ctx, conn, "contact_plan", "contact_plan_active_key",
			"(plan_key)", "status"); err != nil {
			return err
		}
		// The unrelated-role invariant is a schema guarantee, not a convention. Startup refuses
		// to run against a database where it has been dropped.
		var invariant bool
		err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint
		 WHERE conrelid='contact_plan'::regclass AND contype='c' AND convalidated
		   AND pg_get_constraintdef(oid) LIKE '%specific_job_application%'
		   AND pg_get_constraintdef(oid) LIKE '%unrelated%')`).Scan(&invariant)
		if err != nil {
			return err
		}
		if !invariant {
			return fmt.Errorf("missing unrelated-role contact plan invariant constraint")
		}
	}
	if version >= 23 {
		// The duplicate guard. Without this index two concurrent requests could both reserve the
		// same logical message and transmit it twice, so startup refuses to run without it.
		if err := requirePartialUniqueIndex(ctx, conn, "send_attempt", "send_attempt_live_key",
			"(idempotency_key)", "state"); err != nil {
			return err
		}
		if err := requirePartialUniqueIndex(ctx, conn, "send_approval", "send_approval_live",
			"(draft_id)", "revoked_at"); err != nil {
			return err
		}
		if err := requirePartialUniqueIndex(ctx, conn, "outbound_thread", "outbound_thread_active_recipient",
			"(recipient_contact_id)", "state"); err != nil {
			return err
		}
	}
	if version >= 24 {
		// One current classification per legacy thread. Without this, a correction could leave
		// two live answers about what an old message was for.
		if err := requirePartialUniqueIndex(ctx, conn, "legacy_thread_intent", "legacy_thread_intent_current",
			"(send_log_id)", "superseded_at"); err != nil {
			return err
		}
	}
	return nil
}

func requirePartialUniqueIndex(ctx context.Context, conn *pgx.Conn, table, index, columns, predicate string) error {
	var valid bool
	err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
	 WHERE i.indrelid=$1::regclass AND c.relname=$2 AND i.indisunique AND i.indisvalid
	   AND pg_get_indexdef(i.indexrelid) LIKE '%' || $3 || '%'
	   AND pg_get_expr(i.indpred,i.indrelid) LIKE '%' || $4 || '%')`, table, index, columns, predicate).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("missing/invalid partial unique index %s on %s", index, table)
	}
	return nil
}
