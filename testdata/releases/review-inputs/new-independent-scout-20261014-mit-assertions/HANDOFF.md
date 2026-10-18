# M3-02 source contract handoff

**125 new unique positive title inputs, all fixed MIT tests; zero exact overlap with the 878-entry current gold or any of the 166 saved baseline evidence files.** No full new contracts, Pulse probes or gold additions. No tracker-occurrence proof is required or claimed.

The final machine-readable receipt is `handoff-receipt.json`. The earlier `capacity-receipt.json` is an intermediate receipt for 117 new + 5 old reserve and is superseded for final source capacity. Do not count those five old reserve inputs as new.

Read the 125 records as `inventory.json.rows` (117) plus `supplement.json.rows` (8). Keep `input`, `title`, `source_id`, `source_file`, `source_index`, `input_line`, `title_line` and original expected fields/assertion blocks unchanged. `inventory.json.excluded` is not an audit queue. `supplement.json.excluded` is empty.

| Source | Net new cases | Fixed commit | Original title assertion dataflow |
|---|---:|---|---|
| dmachard/ReleascenIFY | 95 | 933b8d07e682331ae194ccc837719b7afc154dc8 | Fixed movie/series JSON filename + literal expected.title → pytest parameter → direct expected-field comparison. Full fixture files and runner read; no expected-data writer found. |
| chad3814/media-name-parser | 21 | 9879051d9bc794da5fd8c07a5f786503c131899c | Handwritten `video.test.ts` / `leadingGroup.test.ts` direct literal title equalities and three literal name/title/group tuples. Three supplementary records use direct real-movie `result.parsed.title` comparisons. Generated corpus gold is excluded; all producers were read and preserved separately. |
| ngtrio/tnp | 4 | 690b6261095899ba0c72d693b5399755e4902fc0 | `TestCommon`: literal Parse input → literal `is.Equal(info.Name, expected)`; raw/formatted duplicates counted once. |
| Bas-Man/TVShowFile | 4 | 2f341c97dcbe52eee0c0e71752173c9e9442450c | Four dated-show test `setUp` literals → direct `getCleanShowName()` equality with handwritten series names; context imports only the local implementation. No externally referenced regex/test data supplies these expected strings. |
| matsimitsu/release_parser | 1 | c75bf77dc8176a6e7ccd5f7d2169202c122408b1 | Literal `let(:input)` for Looped → RSpec literal `result.title == Looped`; helpers/config do not generate expected values. |

Original test/license/README/configuration bytes and SHA-256/length/immutable URLs are in `manifest.json.sources[].files` and `supplement.json.sources[].files`. Supplement Walker files reuse the original preserved bytes. Every imported source is MIT with its complete notice. Search responses preserve `fork: false` metadata; metadata is not a substitute for fixed licenses or proof against every possible shared influence. References.md for TVShowFile records external regex/code references; only its four local MIT date-title tests are admitted here, with no imported referenced code or data.

Exclusions: 25 original assertion occurrences exactly overlap prior evidence, including all 24 extracted C# TorrentTitleParser records; four more occurrences repeat ngtrio parsing modes. No serialized parser output, generated snapshot, synthetic placeholder, restrictive-license dataset or duplicate is counted. Additional discoveries remain unimported and contribute zero capacity.

Review traps retained unchanged: source year/quality fields are declarations, not measurements; title-only omissions are not negative labels. Looped's `E14A` is partial/segment evidence. Dates do not determine canonical TV season/episode units. Treasure Island has competing original/re-release years, so do not adopt an upstream first-year rule. COMPLETE/pack/OVA/special or conflicting coverage requires zero confidence, zero coverage and review, without partial authorization.

The initial snapshot had 864 gold. An external writer raised it to 878 during acquisition; both fingerprints and the explicit drift are retained. At final receipt, **878 + 125 = 1003 potential full records**, without counting the five old reserve cases. This is enough raw source capacity for the requested next 122, not a guarantee of complete-contract eligibility or unchanged parser passes. Main can parallelize individual audits now; freeze each complete manual contract before the first Pulse probe.

Verify from the repository root with the pinned Node, assigning TMPDIR/TMP/TEMP under an owned `/data/temp` subdirectory:

```sh
mise exec -- node testdata/releases/review-inputs/new-independent-scout-20261014-mit-assertions/scout.cjs
```

One private helper statically reproduces the original and supplementary literals, checks source/license/metadata bytes, validates both receipt generations and current baseline hashes, and repeats conservative exact dedup. It imports no upstream or Pulse parser. Later changes to the frozen current baseline cause a verification failure; do not silently rewrite the receipt.
