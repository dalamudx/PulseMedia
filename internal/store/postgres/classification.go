package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/classification"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strconv"
)

type ClassificationStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewClassificationStore(pool *pgxpool.Pool, ids ports.IDGen) *ClassificationStore {
	return &ClassificationStore{pool: pool, ids: ids}
}

var _ ports.ClassificationRepository = (*ClassificationStore)(nil)

func classificationError(ctx context.Context, e error) error {
	for _, v := range []error{ports.ErrInvalidClassification, ports.ErrClassificationConflict, ports.ErrClassificationEvidence} {
		if errors.Is(e, v) {
			return v
		}
	}
	return subscriptionIntentError(ctx, e)
}
func classificationAdmin(ctx context.Context, tx pgx.Tx, a *ports.Actor) error {
	if e := lockAcquisitionUser(ctx, db.New(tx), a); e != nil {
		return e
	}
	if !a.IsAdmin() {
		return ports.ErrForbidden
	}
	return nil
}
func readClassification(ctx context.Context, tx pgx.Tx, version int64) (*ports.ClassificationDocument, error) {
	out := &ports.ClassificationDocument{Rules: []classification.Rule{}}
	var raw []byte
	var e error
	if version == 0 {
		raw, e = db.New(tx).ClassificationReadLatest(ctx)
	} else {
		raw, e = db.New(tx).ClassificationReadVersion(ctx, version)
	}
	if errors.Is(e, pgx.ErrNoRows) && version == 0 {
		return out, nil
	}
	if e != nil {
		return nil, e
	}
	if json.Unmarshal(raw, out) != nil || classification.Validate(out.Rules) != nil {
		return nil, ports.ErrUnavailable
	}
	return out, nil
}
func (s *ClassificationStore) ReadClassification(ctx context.Context, a *ports.Actor) (out *ports.ClassificationDocument, err error) {
	defer func() { err = classificationError(ctx, err) }()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer creationRollback(ctx, tx)
	if e = classificationAdmin(ctx, tx, creationClone(a)); e != nil {
		return nil, e
	}
	if e = db.New(tx).SetSubscriptionListReadOnly(ctx); e != nil {
		return nil, e
	}
	out, e = readClassification(ctx, tx, 0)
	if e != nil {
		return nil, e
	}
	return out, tx.Commit(ctx)
}
func (s *ClassificationStore) PublishClassification(ctx context.Context, a *ports.Actor, in ports.ClassificationPublish, key string) (out *ports.ClassificationDocument, err error) {
	defer func() { err = classificationError(ctx, err) }()
	if in.ExpectedVersion < 0 || classification.Validate(in.Rules) != nil {
		return nil, ports.ErrInvalidClassification
	}
	if _, e := creationKey(key); e != nil {
		return nil, ports.ErrInvalidClassification
	}
	if in.Rules == nil {
		in.Rules = []classification.Rule{}
	}
	in = creationClone(in)
	for i := range in.Rules {
		r := &in.Rules[i]
		if r.Genres == nil {
			r.Genres = []string{}
		}
		if r.Languages == nil {
			r.Languages = []string{}
		}
		if r.Countries == nil {
			r.Countries = []string{}
		}
	}
	a = creationClone(a)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer creationRollback(ctx, tx)
	if e = classificationAdmin(ctx, tx, a); e != nil {
		return nil, e
	}
	if e = db.New(tx).ClassificationLockPublish(ctx); e != nil {
		return nil, e
	}
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	original, e := db.New(tx).ClassificationReadOriginal(ctx, db.ClassificationReadOriginalParams{ActorID: uuid.MustParse(a.ID), RequestKey: key})
	if e == nil {
		if hash != original.RequestHash {
			return nil, ports.ErrClassificationConflict
		}
		out, e = readClassification(ctx, tx, original.Version)
		if e != nil {
			return nil, e
		}
		return out, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	latest, e := readClassification(ctx, tx, 0)
	if e != nil {
		return nil, e
	}
	if latest.Version != in.ExpectedVersion {
		return nil, ports.ErrClassificationConflict
	}
	// Serialize root registration, then lock roots in stable order through the existing bootstrap gate.
	if e = db.New(tx).LockLibraryRootBootstrap(ctx); e != nil {
		return nil, e
	}
	roots := map[uuid.UUID]string{}
	for _, r := range in.Rules {
		if k, ok := roots[r.RootID]; ok && k != r.MediaType {
			return nil, ports.ErrInvalidClassification
		}
		roots[r.RootID] = r.MediaType
	}
	for _, id := range classificationRootIDs(roots) {
		r, e := db.New(tx).LockAcquisitionRoot(ctx, id)
		if e != nil {
			return nil, e
		}
		if r.MediaType != roots[id] || r.Health != "online" {
			return nil, ports.ErrClassificationConflict
		}
	}
	rules, _ := json.Marshal(in.Rules)
	out = &ports.ClassificationDocument{Version: latest.Version + 1, Rules: in.Rules}
	at, e := db.New(tx).ClassificationInsertPolicy(ctx, db.ClassificationInsertPolicyParams{Version: out.Version, Rules: rules, ActorID: uuid.MustParse(a.ID), RequestKey: key, RequestHash: hash})
	if e != nil {
		return nil, e
	}
	out.PublishedAt = &at.Time
	safe, _ := json.Marshal(map[string]int64{"version": out.Version})
	q := db.New(tx)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: "user", ActorID: a.ID, Action: "classification.publish", TargetType: "classification", TargetID: strconv.FormatInt(out.Version, 10), Payload: safe}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "classification", AggregateID: "policy"}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "classification", AggregateID: "policy"})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "classification", AggregateID: "policy", EventType: "classification.published", Payload: safe, Seq: int64(seq)}); e != nil {
		return nil, e
	}
	return out, tx.Commit(ctx)
}
func (s *ClassificationStore) PreviewClassification(ctx context.Context, a *ports.Actor, in ports.ClassificationPreviewInput) (out *ports.ClassificationPreview, err error) {
	defer func() { err = classificationError(ctx, err) }()
	if in.Version < 1 || (in.MediaID == nil) == (in.TMDBID == nil) || in.TMDBID != nil && (*in.TMDBID < 1 || (in.MediaType != "movie" && in.MediaType != "tv")) {
		return nil, ports.ErrInvalidClassification
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, e
	}
	defer creationRollback(ctx, tx)
	if e = lockAcquisitionUser(ctx, db.New(tx), creationClone(a)); e != nil {
		return nil, e
	}
	if e = db.New(tx).SetSubscriptionListReadOnly(ctx); e != nil {
		return nil, e
	}
	doc, e := readClassification(ctx, tx, 0)
	if e != nil {
		return nil, e
	}
	if doc.Version != in.Version {
		return nil, ports.ErrClassificationConflict
	}
	ci := ports.SubscriptionCreationInput{MediaID: in.MediaID, TMDBID: in.TMDBID, MediaType: in.MediaType}
	m, tmdb, e := creationMedia(ctx, db.New(tx), ci)
	if e != nil {
		return nil, e
	}
	facts, e := classificationFacts(ctx, tx, m.ID, m.MediaType)
	if e != nil {
		return nil, e
	}
	if in.TrustedFacts != nil {
		if in.TMDBID == nil || in.TrustedFacts.MediaType != m.MediaType {
			return nil, ports.ErrInvalidClassification
		}
		facts = *in.TrustedFacts
	}
	result := classification.Evaluate(doc.Rules, facts)
	stamp := ""
	if result.RootID != nil {
		r, e := db.New(tx).GetLibraryRootByID(ctx, *result.RootID)
		if errors.Is(e, pgx.ErrNoRows) || e == nil && (r.Health != "online" || r.MediaType != m.MediaType) {
			result.Status = "target_unavailable"
			result.RootID = nil
			result.Reasons = []string{"target_root_unavailable"}
		} else if e != nil {
			return nil, e
		} else {
			stamp = classification.SelectionStamp(doc.Version, classificationIdentity(m.ID, m.MediaType, tmdb), facts, r.ID, r.Generation)
		}
	}
	out = &ports.ClassificationPreview{Version: doc.Version, Result: result, PreviewStamp: stamp}
	observed, e := db.New(tx).ClassificationSnapshotTime(ctx)
	if e != nil {
		return nil, e
	}
	out.ObservedAt = observed.Time
	return out, tx.Commit(ctx)
}

// AuthorizeClassificationPreview ends its read snapshot before provider I/O starts.
func (s *ClassificationStore) AuthorizeClassificationPreview(ctx context.Context, a *ports.Actor, in ports.ClassificationPreviewInput) (err error) {
	defer func() { err = classificationError(ctx, err) }()
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return e
	}
	defer creationRollback(ctx, tx)
	if e = lockAcquisitionUser(ctx, db.New(tx), creationClone(a)); e != nil {
		return e
	}
	if e = db.New(tx).SetSubscriptionListReadOnly(ctx); e != nil {
		return e
	}
	doc, e := readClassification(ctx, tx, 0)
	if e != nil {
		return e
	}
	if doc.Version != in.Version {
		return ports.ErrClassificationConflict
	}
	return tx.Commit(ctx)
}
func classificationFacts(ctx context.Context, tx pgx.Tx, id uuid.UUID, kind string) (classification.Facts, error) {
	f := classification.Facts{MediaType: kind}
	raw, e := db.New(tx).ClassificationReadFacts(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return f, nil
	}
	if e != nil {
		return f, e
	}
	if json.Unmarshal(raw, &f) != nil || f.MediaType != kind {
		return f, ports.ErrUnavailable
	}
	return f, nil
}
