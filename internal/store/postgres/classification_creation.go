package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/classification"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"slices"
	"sort"
	"strings"
	"unicode"
)

func classificationIdentity(id uuid.UUID, kind string, tmdb int64) string {
	if tmdb > 0 {
		return fmt.Sprintf("tmdb:%s:%d", kind, tmdb)
	}
	return "media:" + id.String()
}

// The preview target only admits metadata work; final selection must match freshly trusted facts.
func classificationRequestedRoot(ctx context.Context, tx pgx.Tx, in ports.SubscriptionCreationInput, kind string) (uuid.UUID, error) {
	if in.ClassificationVersion == nil || in.ClassificationPreviewRootID == nil || !creationHash(in.ClassificationPreviewStamp) {
		return uuid.Nil, ports.ErrInvalidSubscriptionBindings
	}
	if e := db.New(tx).ClassificationLockSelection(ctx); e != nil {
		return uuid.Nil, e
	}
	doc, e := readClassification(ctx, tx, 0)
	if e != nil {
		return uuid.Nil, e
	}
	if doc.Version != *in.ClassificationVersion {
		return uuid.Nil, ports.ErrClassificationConflict
	}
	root, e := db.New(tx).GetLibraryRootByID(ctx, *in.ClassificationPreviewRootID)
	if e != nil {
		return uuid.Nil, e
	}
	if root.MediaType != kind || root.Health != "online" {
		return uuid.Nil, ports.ErrClassificationConflict
	}
	return root.ID, nil
}
func classificationRootIDs(roots map[uuid.UUID]string) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(roots))
	for id := range roots {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}
func classificationCreationRoot(ctx context.Context, tx pgx.Tx, in ports.SubscriptionCreationInput, kind string) (uuid.UUID, classification.Result, classification.Facts, error) {
	var result classification.Result
	var facts classification.Facts
	if in.ClassificationVersion == nil || in.RootID != nil {
		return uuid.Nil, result, facts, ports.ErrInvalidSubscriptionBindings
	}
	if e := db.New(tx).ClassificationLockSelection(ctx); e != nil {
		return uuid.Nil, result, facts, e
	}
	doc, e := readClassification(ctx, tx, 0)
	if e != nil {
		return uuid.Nil, result, facts, e
	}
	if doc.Version != *in.ClassificationVersion {
		return uuid.Nil, result, facts, ports.ErrClassificationConflict
	}
	m, tmdb, e := creationMedia(ctx, db.New(tx), in)
	if e != nil {
		return uuid.Nil, result, facts, e
	}
	facts, e = classificationFacts(ctx, tx, m.ID, kind)
	if e != nil {
		return uuid.Nil, result, facts, e
	}
	result = classification.Evaluate(doc.Rules, facts)
	if result.Status == "insufficient_evidence" {
		return uuid.Nil, result, facts, ports.ErrClassificationEvidence
	}
	if result.Status != "matched" || result.RootID == nil {
		return uuid.Nil, result, facts, ports.ErrClassificationConflict
	}
	root, e := db.New(tx).GetLibraryRootByID(ctx, *result.RootID)
	if e != nil {
		return uuid.Nil, result, facts, e
	}
	stamp := classification.SelectionStamp(doc.Version, classificationIdentity(m.ID, kind, tmdb), facts, root.ID, root.Generation)
	if in.ClassificationPreviewRootID == nil || root.ID != *in.ClassificationPreviewRootID || stamp != in.ClassificationPreviewStamp {
		return uuid.Nil, result, facts, ports.ErrClassificationConflict
	}
	return *result.RootID, result, facts, nil
}
func persistCreationClassificationFacts(ctx context.Context, tx pgx.Tx, id uuid.UUID, p *ports.PreparedSubscriptionCreationMetadata) error {
	if p == nil || p.ClassificationFacts == nil {
		return nil
	}
	installed, e := db.New(tx).ClassificationFactsInstalled(ctx)
	if e != nil {
		return e
	}
	if !installed {
		return nil
	}
	f := *p.ClassificationFacts
	if f.MediaType != p.Media.MediaType {
		return ports.ErrInvalidSubscriptionBindings
	}
	for _, set := range [][]string{f.Genres, f.Languages, f.Countries} {
		if len(set) > 64 {
			return ports.ErrInvalidSubscriptionBindings
		}
		for _, v := range set {
			if v == "" || len(v) > 64 || strings.TrimSpace(v) != v || strings.ContainsFunc(v, unicode.IsControl) {
				return ports.ErrInvalidSubscriptionBindings
			}
		}
	}
	check := classification.Rule{ID: "metadata", MediaType: f.MediaType, RootID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Languages: f.Languages, Countries: f.Countries}
	if classification.Validate([]classification.Rule{check}) != nil {
		return ports.ErrInvalidSubscriptionBindings
	}
	// Preserve an explicit empty known set; nil means that provider evidence was missing.
	f.Genres = slices.Clone(f.Genres)
	raw, e := json.Marshal(f)
	if e != nil {
		return e
	}
	e = db.New(tx).ClassificationUpsertFacts(ctx, db.ClassificationUpsertFactsParams{MediaID: id, Facts: raw})
	return e
}
func freezeCreationClassification(ctx context.Context, tx pgx.Tx, sub, root uuid.UUID, generation int64, in ports.SubscriptionCreationInput, kind string) error {
	selected, result, facts, e := classificationCreationRoot(ctx, tx, in, kind)
	if e != nil {
		return e
	}
	if selected != root {
		return ports.ErrClassificationConflict
	}
	raw, _ := json.Marshal(result)
	fraw, _ := json.Marshal(facts)
	e = db.New(tx).ClassificationFreezeChoice(ctx, db.ClassificationFreezeChoiceParams{SubscriptionID: sub, PolicyVersion: *in.ClassificationVersion, RootID: root, RootGeneration: generation, Result: raw, Facts: fraw})
	return e
}
