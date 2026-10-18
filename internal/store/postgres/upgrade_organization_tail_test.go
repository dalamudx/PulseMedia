package postgres_test

import (
	"context"
	"testing"
)

func TestMovieUpgradeOrganizationTailAuthorityPG(t *testing.T) {
	for _, mode := range []string{"account", "tracking_receipt"} {
		t.Run(mode, func(t *testing.T) {
			f := nativeOrganizationFixture(t, "completed")
			ctx := context.Background()
			c := f.command(t)
			before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			mutation := `UPDATE app_user SET is_active=false WHERE id=(SELECT actor_id FROM movie_upgrade_organization_authorization WHERE id=NEW.aggregate_id::uuid);`
			if mode == "tracking_receipt" {
				mutation = `UPDATE idempotency_record SET response_body='{}'::jsonb WHERE idempotency_key LIKE 'upgrade.tracking-result:%';`
			}
			if _, e := f.p.Exec(ctx, `CREATE FUNCTION pulse_test_org_authority_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='upgrade.organization-authorize' THEN `+mutation+` END IF; RETURN NEW;END $$; CREATE TRIGGER pulse_test_org_authority_tail AFTER INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION pulse_test_org_authority_tail()`); e != nil {
				t.Fatal(e)
			}
			if _, e := f.organization.AuthorizeMovieUpgradeOrganization(ctx, f.actor, c, "authority-tail"); e == nil {
				t.Fatal("transaction-tail authority change accepted")
			}
			f.unchanged(t, before, 0)
		})
	}
}
func organizationAssertFiles(t *testing.T, f *organizationFixture) func() {
	t.Helper()
	snapshot := func() string {
		var s string
		e := f.p.QueryRow(context.Background(), `SELECT encode(sha256(convert_to(jsonb_build_object('files',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM library_file x),'mappings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM library_file_unit x),'facts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_fact x),'plans',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM transfer_plan x),'steps',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM transfer_step x))::text,'UTF8')),'hex')`).Scan(&s)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	before := snapshot()
	return func() {
		t.Helper()
		if snapshot() != before {
			t.Fatal("original files/mappings/facts/transfer graph changed")
		}
	}
}
