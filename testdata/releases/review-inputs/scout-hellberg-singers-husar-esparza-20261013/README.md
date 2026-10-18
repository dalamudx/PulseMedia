# New independent MIT title fixtures — source scout

This isolated addition preserves 126 different realistic raw filename/release examples with direct, hand-maintained literal title expectations from four previously absent test suites. They are pending review, not full gold. No upstream parser, test runner, generator, installer or dependency was executed. No existing shared repository file was edited.

| Source | Fixed commit | Direct title assertions | Exact overlap with gold | Exact overlap with existing source evidence |
|---|---|---:|---:|---:|
| peterhellberg/release | 681f91c2b326b7722fbae94f0ae27301c6df05f9 | 85 | 0 | 0 |
| matiassingers/scene-release | b1a01a759d94e78e80c7d8308571808ff85bcf8c | 14 | 0 | 0 |
| danielhusar/movie-title | 917e4bb4c0b2e9c61b7a2e0a1086496e71c9cbaf | 24 | 0 | 0 |
| danesparza/dlshow | 411a1b38da88268107d2c29a418d74afb2cfd13f | 3 | 0 | 0 |

All four preserve the complete original MIT license, original test bytes as `.source`, and original README bytes as `.source`. `manifest.json` records immutable blob URLs, SHA-256, lengths, fixed commits, and expected-dataflow. `inventory.json` preserves each input/title spelling, source row and line numbers, explicitly supplied year/season/episode labels where extracted, and all 149 baseline evidence file hashes. The inventory deliberately does not map original technical fields into PulseMedia quality or coverage. Missing years are not inferred. An original zero-year value remains the original assertion, not proof that a work has no release year. Colbert's year is specifically an airdate year.

The Go release suite compares literal `releaseTests` input/title/year fields with `Parse(tt.Input)` results; the JavaScript suites compare parser results directly with literal titles; dlshow compares explicit table names and a separately asserted Colbert Report title. These are independent expected-data paths, not serialized parser-output gold. The fixed tests and READMEs show separate authorship and no import of the existing fixture suites. Scene-release's links to similar projects do not supply its expected values. There is no claim that every example was observed on a tracker or that authorship history proves the absence of every possible shared influence.

Exact deduplication uses unnormalized strings against the frozen 687-entry `reviewed-full.json` and a conservative universe of all persisted JSON values/keys and source literals under `testdata/releases`, including excluded cases and moistari probe evidence. A second verification checks raw-text occurrences independently of lexical scanning. Result: 126 unique inputs, zero internal duplicates, zero matches with existing gold or saved source evidence. Baseline snapshots were unchanged during verification. This is a census of these new candidates, not a new all-source capacity or admission claim.

Some original TV title expectations include SxxExx or INTERNAL text; these remain unchanged and are marked for review. Complete/pack examples also remain pending and grant no coverage, confidence, canonical-unit, acquisition or execution authority. All manifests have `reviewed_entries: 0`, `accepted: false`, `authority: false`, `coverage_authority: false`, `canonical_unit_authorization: false`, and `formal_golden_acceptance: false`. Newly completed gold: **0**.

Research also inspected go-ptt (PTT/Beast acknowledgements), Hunch (GuessIt-derived LGPL fixtures), Cinekey (Hunch/Oleoo public sets and private local collection), WeebFileNameParser (interactive test console), Majestixx (unresolved related-fixture provenance), TVShowFile (small mixed synthetic tests), and Subliminal (MIT manual objects, but not all tied to filename/title equality). None of those bytes were imported. MPL anitopy/anitomy, GPL/Apache datasets, RTN/Jhin generated-output gold, and duplicate forks were not admitted. This scout found 126 suitable pending literals, not hundreds; it does not claim an exhaustive internet search.

Run the dependency-free static verifier from the project root with the mise-pinned Node:

```sh
mise exec -- node testdata/releases/review-inputs/scout-hellberg-singers-husar-esparza-20261013/verify.cjs
```

The verifier checks all 12 original byte hashes, license notices, all input/title source line literals, authority flags, counts, the inventory digest, and exact overlap against the frozen 149-file baseline. It reports baseline drift rather than silently substituting later corpus contents. GitHub repository metadata lookup was rate-limited; commit identities and preserved bytes were obtained through Git, not inferred from a mutable default-branch URL. Task-owned clones and scratch scripts were removed after preservation.
