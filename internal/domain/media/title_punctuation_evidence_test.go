package media_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// The canonical title remains the immutable independent source assertion.
// This proof permits only a raw, dotted uppercase-initial token in its place;
// it is not a general punctuation normalization or parser-generated oracle.
type titlePunctuationEvidence struct {
	RawInitials   string `json:"raw_initials"`
	ReviewedTitle string `json:"reviewed_title"`
}

var reviewedInitialsEvidenceRegex = regexp.MustCompile(`^(?:[A-Z]\.){2,9}$`)

func validatedInitialsReviewTitle(input, canonical string, evidence titlePunctuationEvidence) (string, error) {
	token := evidence.RawInitials
	if !reviewedInitialsEvidenceRegex.MatchString(token) || strings.Count(input, token) != 1 || strings.Count(evidence.ReviewedTitle, token) != 1 {
		return "", fmt.Errorf("dotted title initials lack unique verbatim raw evidence")
	}
	spaced := strings.Join(strings.Split(strings.TrimSuffix(token, "."), "."), " ")
	if strings.Replace(evidence.ReviewedTitle, token, spaced, 1) != canonical {
		return "", fmt.Errorf("punctuation evidence changed independent title words or case")
	}
	return evidence.ReviewedTitle, nil
}

func TestInitialsTitleEvidenceCannotRewriteIndependentAssertions(t *testing.T) {
	raw := "Marvel's.Agents.of.S.H.I.E.L.D.S02E01.1080p"
	canonical := "Marvel's Agents of S H I E L D"
	good := titlePunctuationEvidence{RawInitials: "S.H.I.E.L.D.", ReviewedTitle: "Marvel's Agents of S.H.I.E.L.D."}
	got, err := validatedInitialsReviewTitle(raw, canonical, good)
	if err != nil || got != good.ReviewedTitle {
		t.Fatal(got, err)
	}
	for _, tc := range []struct {
		raw, canonical string
		proof          titlePunctuationEvidence
	}{
		{raw, canonical, titlePunctuationEvidence{"S.H.I.E.L.D.", "Other Agents of S.H.I.E.L.D."}},
		{raw, canonical, titlePunctuationEvidence{"S.H.I.E.L.D.", "marvel's Agents of S.H.I.E.L.D."}},
		{raw, canonical, titlePunctuationEvidence{"S.H.I.E.L.D.", "Marvel's Agents of S.H.I.E.L.D"}},
		{raw, canonical, titlePunctuationEvidence{"S.H.I.E.L.D.", "Marvel's Agents of S.H.I.E.L.D. S01E02"}},
		{raw, canonical, titlePunctuationEvidence{"S.H.I.E.L.D.", "Marvel's Agents of S.H.I.E.L.D. 2020"}},
		{raw, canonical, titlePunctuationEvidence{"X.Y.Z.", "Marvel's Agents of X.Y.Z."}},
		{raw + ".S.H.I.E.L.D.", canonical, good},
		{"[S.H.I.E.L.D.] raw", canonical, titlePunctuationEvidence{"SH.I.E.L.D.", good.ReviewedTitle}},
		{"The.Man.from.U.N.C.L.E.", "The Man from U.N.C.L.E.", titlePunctuationEvidence{"U.N.C.L.E.", "The Man from U.N.C.L.E."}},
	} {
		if _, err := validatedInitialsReviewTitle(tc.raw, tc.canonical, tc.proof); err == nil {
			t.Fatal("changed independent assertion accepted", tc)
		}
	}
}
