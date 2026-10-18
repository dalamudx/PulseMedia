# Independent MIT source acquisition — 2026-10-14

The final source-only handoff contains **125 newly acquired unique realistic inputs with direct fixed literal title/name assertions**, with zero exact overlap against the receipt's 878-entry gold or any of its 166 saved evidence files. New complete contracts, Pulse probes and gold additions are all **0**. No upstream parser, test, generator, installer or dependency was run.

Start with **[HANDOFF.md](HANDOFF.md)** for the compact source contract, qualifying exclusions, title-oracle dataflow, coverage traps and audit instructions. `handoff-receipt.json` is the final machine-readable receipt. Its queue is `inventory.json.rows` (117) plus `supplement.json.rows` (8). Do not audit excluded occurrences or count existing reserve as newly acquired input.

| Source | New unique inputs |
|---|---:|
| dmachard/ReleascenIFY | 95 |
| chad3814/media-name-parser, selected handwritten unit tests only | 21 |
| ngtrio/tnp | 4 |
| Bas-Man/TVShowFile, selected direct dated-show title tests | 4 |
| matsimitsu/release_parser | 1 |
| Total | 125 |

Each record preserves the exact input/title, original source file/index and original assertion lines. Original expected fields/assertion blocks remain unchanged. `manifest.json` and `supplement.json` provide commit-pinned URLs, byte lengths, SHA-256, complete MIT notices, test/README/configuration files and the expected-value flow. Source bytes are under `sources/`. All selected tests and relevant expectation-generation scripts/configuration were read completely. Git test histories are separately marked scout-generated evidence. Preserved GitHub search responses under `metadata/` show the selected repositories as non-forks with MIT metadata; fixed license bytes remain the licensing evidence. This does not claim proof against every possible shared influence or require tracker-occurrence evidence.

The 154 direct title-assertion occurrences produce 125 unique new inputs after excluding 25 existing-input overlaps and four repeated ngtrio modes. All 24 extracted bhmahler/TorrentTitleParser title inputs are already covered by the old titleparser evidence and add zero new capacity. Their original test/license bytes are retained to make that exclusion reviewable. No generated snapshots, serialized parser-output gold, synthetic placeholders, duplicate forks or BSD/GPL/LGPL/MPL/Apache fixture data are admitted.

Walker's generated corpus pipeline is explicitly excluded. All producer scripts and their test consumer are preserved as non-executed evidence; they write generated corpus fixtures, not the selected handwritten unit declarations. TVShowFile's References.md lists external regex/code references, but the four selected local MIT tests bind their own literal filenames directly to independent literal clean-title expectations; no referenced implementation or external test data is imported. Missing source fields are not negative assertions. Date names do not establish canonical seasons/episodes; Looped E14A and conflicting/re-release years need explicit review contracts. Complete/pack/OVA/special or ambiguous coverage must not retain partial confidence, coverage or execution authority.

The first snapshot had 864 gold. During acquisition an external writer changed only `reviewed-full.json` among the 166 baseline files, raising gold to 878. The immutable initial inventory keeps its original fingerprint, and the receipt explicitly records the later snapshot and drift. This task did not edit any existing shared corpus, checker, document or configuration.

The earlier `capacity-receipt.json` records the intermediate 117-new-plus-5-existing-reserve state and validates against the archived initial helper bytes in `scout.initial.cjs.source`. It is retained as an intermediate checkpoint. The final `handoff-receipt.json` validates the current single executable helper and **125 new inputs alone**, giving **878 + 125 = 1003 potential complete records**. This meets the requested next-122 source capacity with three spare cases; it does not claim full-contract admission, passing parser cases or M3-02 acceptance.

Verify from the repository root with the pinned Node and TMPDIR/TMP/TEMP assigned under an owned `/data/temp` directory:

```sh
mise exec -- node testdata/releases/review-inputs/new-independent-scout-20261014-mit-assertions/scout.cjs
```

The private dependency-free helper reproduces both literal extractions and positions, checks licenses/source/configuration/metadata/history bytes, verifies receipt hashes and the full current baseline, and repeats conservative exact dedup against decoded JSON/JSONL strings and keys, escaped literals, serialized spellings and raw-text occurrences. Later changes to the frozen current baseline fail verification rather than silently substituting another snapshot.

Main can parallelize individual annotation now. Freeze a complete per-record manual contract before any Pulse probe; keep source labels unchanged and do not derive expectations from parser output. No parser pass/defect count is available for this source-only handoff. Task-owned downloads, clones and scratch files were removed after verification; no upstream process remains running.
