package media

import (
	"errors"
	"github.com/google/uuid"
	"slices"
	"testing"
)

// Self-authored safety controls, not golden parser samples or a source corpus.
func movieUpgradeTargetFixture() MovieUpgradeTarget {
	return MovieUpgradeTarget{MediaID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), UnitID: uuid.MustParse("22222222-2222-4222-8222-222222222222"), MediaVersion: 3, UnitVersion: 4, MediaType: TypeMovie, UnitKey: "movie", OriginalTitle: "Fight Club", Year: 1999, Identities: []Identity{{Namespace: "tmdb.movie", ExternalID: "550"}}, Aliases: []string{"搏击俱乐部"}, ParserVersion: EpisodeParserVersion}
}
func TestMovieUpgradeFilesAccepted(t *testing.T) {
	for name, files := range map[string][]TVTorrentFile{
		"single":           {{"Fight.Club.1999.mkv", 4}},
		"directory":        {{"Fight.Club.1999/Fight.Club.1999.mkv", 4}, {"Fight.Club.1999/readme.txt", 0}, {"Fight.Club.1999/Subs/en.srt", 0}, {"Fight.Club.1999/poster.jpg", 0}},
		"controlled_alias": {{"Fight Club/搏击俱乐部.1999.mkv", 4}},
		"matching_ids":     {{"Fight.Club.1999.{tmdb-550}.mkv", 4}},
	} {
		t.Run(name, func(t *testing.T) {
			target := movieUpgradeTargetFixture()
			before := slices.Clone(files)
			got, err := ValidateMovieUpgradeTorrentFiles(files, target)
			if err != nil || got == nil || got.Path != files[0].Path || got.Size != 4 || got.MediaID != target.MediaID || got.UnitID != target.UnitID || got.MediaVersion != 3 || got.UnitVersion != 4 || !slices.Equal(before, files) {
				t.Fatalf("unexpected evidence: %+v %v", got, err)
			}
		})
	}
}
func TestMovieUpgradeFilesRefuseUnsafeInventory(t *testing.T) {
	for name, files := range map[string][]TVTorrentFile{
		"missing":                    nil,
		"only_attachment":            {{"readme.txt", 0}},
		"second_work":                {{"Fight.Club.1999.mkv", 4}, {"Inception.2010.mkv", 4}},
		"duplicate_video":            {{"Fight.Club.1999.mkv", 4}, {"搏击俱乐部.1999.mp4", 4}},
		"same_title_alias_collision": {{"Fight.Club.1999.mkv", 4}, {"FIGHT.CLUB.1999.MKV", 4}},
		"duplicate_zero_attachment":  {{"Fight.Club.1999.mkv", 4}, {"poster.jpg", 0}, {"poster.jpg", 0}},
		"parent_second_work":         {{"Inception.2010/Fight.Club.1999.mkv", 4}},
		"deep_conflict":              {{"Fight Club/Inception.2010/Fight.Club.1999.mkv", 4}},
		"parent_wrong_year":          {{"Fight.Club.2000/Fight.Club.1999.mkv", 4}},
		"parent_unknown":             {{"downloads/Fight.Club.1999.mkv", 4}},
		"parent_tv":                  {{"S01E01/Fight.Club.1999.mkv", 4}},
		"parent_range":               {{"Show.S01E01-E03/Fight.Club.1999.mkv", 4}},
		"parent_collection":          {{"Fight Club 3 Movie Collection/Fight.Club.1999.mkv", 4}},
		"parent_split_rar":           {{"Fight.Club.1999.part01.rar/Fight.Club.1999.mkv", 4}},
		"parent_sample":              {{"sample/Fight.Club.1999.mkv", 4}},
		"parent_ova":                 {{"OVA/Fight.Club.1999.mkv", 4}},
		"parent_part":                {{"part1/Fight.Club.1999.mkv", 4}},
		"unknown_video":              {{"movie.mkv", 4}},
		"no_year":                    {{"Fight.Club.mkv", 4}},
		"wrong_year":                 {{"Fight.Club.2000.mkv", 4}},
		"ambiguous_year":             {{"Fight.Club.1999.2000.mkv", 4}},
		"matching_id_wrong_title":    {{"Inception.1999.{tmdb-550}.mkv", 4}},
		"foreign_id":                 {{"Fight.Club.1999.{tmdb-551}.mkv", 4}},
		"second_id":                  {{"Fight.Club.1999.{tmdb-550}.{tmdb-551}.mkv", 4}},
		"sample":                     {{"Fight.Club.1999.sample.mkv", 4}},
		"part":                       {{"Fight.Club.1999.part1.mkv", 4}},
		"ova":                        {{"Fight.Club.1999.OVA.mkv", 4}},
		"tv":                         {{"Fight.Club.1999.S01E01.mkv", 4}},
		"mixed_coverage":             {{"Fight.Club.1999.S01E01E03.mkv", 4}},
		"empty_video":                {{"Fight.Club.1999.mkv", 0}},
		"negative":                   {{"Fight.Club.1999.mkv", -1}},
		"traversal":                  {{"../Fight.Club.1999.mkv", 4}},
		"absolute":                   {{"/Fight.Club.1999.mkv", 4}},
		"backslash":                  {{"Fight Club\\Fight.Club.1999.mkv", 4}},
		"double_slash":               {{"Fight Club//Fight.Club.1999.mkv", 4}},
		"drive":                      {{"C:/Fight.Club.1999.mkv", 4}},
		"nul":                        {{"Fight Club\x00/Fight.Club.1999.mkv", 4}},
		"trailing_dot":               {{"Fight Club./Fight.Club.1999.mkv", 4}},
		"device":                     {{"Fight.Club.1999.mkv", 4}, {"CON.txt", 0}},
		"collision":                  {{"Fight.Club.1999.mkv", 4}, {"Fight.Club.1999.mkv/readme.txt", 0}},
		"unknown_attachment":         {{"Fight.Club.1999.mkv", 4}, {"install.exe", 0}},
		"archive":                    {{"Fight.Club.1999.mkv", 4}, {"Fight.Club.1999.zip", 0}},
		"split_archive":              {{"Fight.Club.1999.mkv", 4}, {"Fight.Club.1999.part01.rar", 0}},
		"second_work_sidecar":        {{"Fight.Club.1999.mkv", 4}, {"Inception.2010.nfo", 0}},
		"sidecar_parent_conflict":    {{"Fight.Club.1999.mkv", 4}, {"Inception.2010/readme.txt", 0}},
		"sidecar_tv":                 {{"Fight.Club.1999.mkv", 4}, {"Fight.Club.S01E01.srt", 0}},
		"unsafe_attachment_parent":   {{"Fight.Club.1999.mkv", 4}, {"Fight Club/Sample/en.srt", 0}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ValidateMovieUpgradeTorrentFiles(files, movieUpgradeTargetFixture())
			if got != nil || !errors.Is(err, ErrMovieUpgradeFilesReview) {
				t.Fatalf("unsafe coverage accepted: %+v %v", got, err)
			}
		})
	}
}
func TestMovieUpgradeFilesRefuseInvalidTarget(t *testing.T) {
	for name, change := range map[string]func(*MovieUpgradeTarget){
		"zero":               func(t *MovieUpgradeTarget) { *t = MovieUpgradeTarget{} },
		"tv":                 func(t *MovieUpgradeTarget) { t.MediaType = TypeTV },
		"wrong_key":          func(t *MovieUpgradeTarget) { t.UnitKey = "S01E01" },
		"old_parser":         func(t *MovieUpgradeTarget) { t.ParserVersion = "episode.v73" },
		"unknown_version":    func(t *MovieUpgradeTarget) { t.UnitVersion = 0 },
		"no_media":           func(t *MovieUpgradeTarget) { t.MediaID = uuid.Nil },
		"no_unit":            func(t *MovieUpgradeTarget) { t.UnitID = uuid.Nil },
		"no_identity":        func(t *MovieUpgradeTarget) { t.Identities = nil },
		"duplicate_identity": func(t *MovieUpgradeTarget) { t.Identities = append(t.Identities, t.Identities[0]) },
		"tv_identity":        func(t *MovieUpgradeTarget) { t.Identities[0].Namespace = "tmdb.tv" },
		"missing_title":      func(t *MovieUpgradeTarget) { t.OriginalTitle = "" },
		"missing_year":       func(t *MovieUpgradeTarget) { t.Year = 0 },
		"same_name_alias":    func(t *MovieUpgradeTarget) { t.Aliases = []string{"FIGHT CLUB"} },
	} {
		t.Run(name, func(t *testing.T) {
			target := movieUpgradeTargetFixture()
			change(&target)
			got, err := ValidateMovieUpgradeTorrentFiles([]TVTorrentFile{{"Fight.Club.1999.mkv", 4}}, target)
			if got != nil || !errors.Is(err, ErrMovieUpgradeFilesReview) {
				t.Fatal("invalid target accepted", got, err)
			}
		})
	}
}
