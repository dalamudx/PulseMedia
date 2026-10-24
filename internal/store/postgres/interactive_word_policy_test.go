package postgres_test

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func interactiveWordPolicy(t *testing.T, p *pgxpool.Pool) (*policy.Service, quality.PolicyRules) {
	t.Helper()
	service := policy.NewService(postgres.NewPolicyStore(p, interactiveIDs{}))
	doc, err := service.Read(t.Context(), "default", 0)
	if err != nil {
		t.Fatal("read stored default", err)
	}
	return service, doc.Rules
}

func wordPolicySnapshot(t *testing.T, p *pgxpool.Pool, tables ...string) map[string][32]byte {
	t.Helper()
	if len(tables) == 0 {
		tables = []string{"policy", "policy_revision", "subscription", "wanted_item", "idempotency_record", "job", "audit_log", "outbox_event", "interactive_search", "interactive_search_source"}
	}
	result := make(map[string][32]byte, len(tables))
	for _, table := range tables {
		var rows string
		if err := p.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb)::text FROM `+table+` r`).Scan(&rows); err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		result[table] = sha256.Sum256([]byte(rows))
	}
	return result
}

func TestInteractiveWordsFreezePublishedPolicyPG(t *testing.T) {
	p, _, actor, store := interactiveFixture(t)
	interactiveSite(t, p, "Site", "online", true, false)
	old := interactiveCreate(t, store, actor, "before-words")
	service, rules := interactiveWordPolicy(t, p)
	rules.CustomWords = []quality.CustomWordRule{{Word: "Atmos", Score: 200}}
	command := ports.PolicyCommand{Kind: "publish", ID: "default", ExpectedRevision: 1, Rules: &rules}
	if receipt, err := service.Command(t.Context(), actor, command, "words-v2"); err != nil || receipt.Revision != 2 {
		t.Fatalf("publish words: %v", err)
	}
	in := interactiveInput()
	in.RankingPolicy.CustomWords = []quality.CustomWordRule{{Word: "caller-only", Score: 1000}}
	header, err := store.CreateInteractiveSearch(t.Context(), actor, in, "with-words", interactiveHash)
	if err != nil || header.RankingPolicy.Revision != 2 || !reflect.DeepEqual(header.RankingPolicy.CustomWords, rules.CustomWords) {
		t.Fatalf("new strategy not read from storage: %v", err)
	}
	var frozen string
	if err = p.QueryRow(t.Context(), `SELECT ranking_policy::text FROM interactive_search WHERE id=$1`, header.ID).Scan(&frozen); err != nil {
		t.Fatal(err)
	}
	next := rules
	next.CustomWords = []quality.CustomWordRule{{Word: "DV", Required: true}}
	if _, err = service.Command(t.Context(), actor, ports.PolicyCommand{Kind: "publish", ID: "default", ExpectedRevision: 2, Rules: &next}, "words-v3"); err != nil {
		t.Fatal(err)
	}
	before := wordPolicySnapshot(t, p)
	replay, err := store.CreateInteractiveSearch(t.Context(), actor, in, "with-words", interactiveHash)
	if err != nil || replay.ID != header.ID || replay.RankingPolicy.Revision != 2 || !reflect.DeepEqual(replay.RankingPolicy.CustomWords, rules.CustomWords) {
		t.Fatalf("replay rebound strategy: %v", err)
	}
	first, err := store.ReadInteractiveSearch(t.Context(), actor, old.ID)
	if err != nil || first.Header.RankingPolicy.Revision != 1 || len(first.Header.RankingPolicy.CustomWords) != 0 {
		t.Fatalf("legacy strategy changed: %v", err)
	}
	var after string
	if err = p.QueryRow(t.Context(), `SELECT ranking_policy::text FROM interactive_search WHERE id=$1`, header.ID).Scan(&after); err != nil || after != frozen {
		t.Fatal("immutable strategy bytes changed")
	}
	if !reflect.DeepEqual(before, wordPolicySnapshot(t, p)) {
		t.Fatal("replay or GET changed protected rows")
	}
	fresh := interactiveCreate(t, store, actor, "after-words-v3")
	if fresh.RankingPolicy.Revision != 3 || !reflect.DeepEqual(fresh.RankingPolicy.CustomWords, next.CustomWords) {
		t.Fatal("fresh strategy did not follow published version")
	}
}

func TestInteractiveWordsFreezeBoundSubscriptionPolicyPG(t *testing.T) {
	p, _, actor, store := interactiveFixture(t)
	interactiveSite(t, p, "Site", "online", true, false)
	service, rules := interactiveWordPolicy(t, p)
	rules.CustomWords = []quality.CustomWordRule{{Word: "国语", Required: true, Score: 100}}
	if _, err := service.Command(t.Context(), actor, ports.PolicyCommand{Kind: "create", ID: "scoped-words", Name: "Scoped words", Rules: &rules}, "create-scoped-words"); err != nil {
		t.Fatal(err)
	}
	mediaID, rootID, subID := uuid.New(), uuid.New(), uuid.New()
	interactiveExec(t, p, `INSERT INTO media(id,title,media_type) VALUES($1,'Movie','movie')`, mediaID)
	interactiveExec(t, p, `INSERT INTO library_root(id,path,media_type,name) VALUES($1,'/configured-word-scope','movie','root')`, rootID)
	interactiveExec(t, p, `INSERT INTO subscription(id,user_id,media_id,root_id,quality_policy_id,quality_policy_revision) VALUES($1,$2,$3,$4,'scoped-words',1)`, subID, actor.ID, mediaID, rootID)
	in := interactiveInput()
	in.SubscriptionID, in.MediaID = &subID, &mediaID
	header, err := store.CreateInteractiveSearch(t.Context(), actor, in, "scoped-word-search", interactiveHash)
	if err != nil || header.RankingPolicy.ID != "scoped-words" || header.RankingPolicy.Revision != 1 || !reflect.DeepEqual(header.RankingPolicy.CustomWords, rules.CustomWords) {
		t.Fatalf("scope policy not frozen: %v", err)
	}
	before := wordPolicySnapshot(t, p)
	if _, err = store.ReadInteractiveSearch(t.Context(), actor, header.ID); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, wordPolicySnapshot(t, p)) {
		t.Fatal("scope GET wrote protected state")
	}
}

func TestInteractiveWordsOriginalReceiptBeforeCurrentPolicyReadPG(t *testing.T) {
	p, _, actor, store := interactiveFixture(t)
	interactiveSite(t, p, "Site", "online", true, false)
	header := interactiveCreate(t, store, actor, "policy-offline-receipt")
	interactiveExec(t, p, `DROP TABLE policy_revision CASCADE`)
	tables := []string{"job", "audit_log", "outbox_event", "idempotency_record", "interactive_search", "interactive_search_source"}
	before := wordPolicySnapshot(t, p, tables...)
	replay, err := store.CreateInteractiveSearch(t.Context(), actor, interactiveInput(), "policy-offline-receipt", interactiveHash)
	if err != nil || replay.ID != header.ID || !reflect.DeepEqual(replay.RankingPolicy, header.RankingPolicy) {
		t.Fatalf("original receipt read current policy: %v", err)
	}
	if _, err = store.CreateInteractiveSearch(t.Context(), actor, interactiveInput(), "new-policy-offline", interactiveHash); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("missing policy became a default success: %v", err)
	}
	if !reflect.DeepEqual(before, wordPolicySnapshot(t, p, tables...)) {
		t.Fatal("failed new observation or replay wrote protected state")
	}
}

func TestPolicyWordsTailRollbackAndStableReceiptPG(t *testing.T) {
	p, _, actor, _ := interactiveFixture(t)
	service, rules := interactiveWordPolicy(t, p)
	rules.CustomWords = []quality.CustomWordRule{{Word: "Atmos", Score: 200}, {Word: "CAM", Excluded: true}}
	command := ports.PolicyCommand{Kind: "publish", ID: "default", ExpectedRevision: 1, Rules: &rules}
	interactiveExec(t, p, `CREATE FUNCTION pulse_word_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key='word-tail' THEN RAISE EXCEPTION 'word-tail'; END IF; RETURN NEW; END $$; CREATE TRIGGER pulse_word_tail BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION pulse_word_tail()`)
	before := wordPolicySnapshot(t, p)
	if _, err := service.Command(t.Context(), actor, command, "word-tail"); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("tail failure classification: %v", err)
	}
	if !reflect.DeepEqual(before, wordPolicySnapshot(t, p)) {
		t.Fatal("tail failure partially published words, revision, audit, event or receipt")
	}
	interactiveExec(t, p, `DROP TRIGGER pulse_word_tail ON idempotency_record; DROP FUNCTION pulse_word_tail()`)
	receipt, err := service.Command(t.Context(), actor, command, "word-tail")
	if err != nil || receipt.Revision != 2 {
		t.Fatalf("original command not retryable: %v", err)
	}
	before = wordPolicySnapshot(t, p)
	replay, err := service.Command(t.Context(), actor, command, "word-tail")
	if err != nil || *replay != *receipt || !reflect.DeepEqual(before, wordPolicySnapshot(t, p)) {
		t.Fatalf("policy original receipt changed: %v", err)
	}
	stored, err := service.Read(t.Context(), "default", 2)
	if err != nil || !reflect.DeepEqual(stored.Rules.CustomWords, rules.CustomWords) {
		t.Fatal("words not persisted in immutable revision")
	}
	old, err := service.Read(t.Context(), "default", 1)
	if err != nil || len(old.Rules.CustomWords) != 0 {
		t.Fatal("historical policy rewritten")
	}
	raw, err := json.Marshal(stored.Rules)
	var decoded quality.PolicyRules
	if err != nil || json.Unmarshal(raw, &decoded) != nil || !reflect.DeepEqual(decoded.CustomWords, rules.CustomWords) {
		t.Fatal("stored word rule encoding not recoverable")
	}
}
