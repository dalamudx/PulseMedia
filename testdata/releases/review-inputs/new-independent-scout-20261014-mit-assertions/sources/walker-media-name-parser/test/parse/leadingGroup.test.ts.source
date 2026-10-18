import { test } from 'node:test';
import assert from 'node:assert/strict';
import { extractLeadingGroup } from '../../lib/parse/ids';
import { parseVideo } from '../../lib/parse/video';

test('a leading bracketed run in any of the three styles is a group', () => {
  // Anime releases put the fansub group first, and the three delimiter
  // styles are all in use by different groups.
  assert.deepEqual(extractLeadingGroup('[Onalrie] ReZero kara Hajimeru Isekai Seikatsu - S04E18'),
    { group: 'Onalrie', rest: 'ReZero kara Hajimeru Isekai Seikatsu - S04E18' });
  assert.deepEqual(extractLeadingGroup('(Erai-raws) Bleach - S01E05'),
    { group: 'Erai-raws', rest: 'Bleach - S01E05' });
  assert.deepEqual(extractLeadingGroup('{Judas} Jujutsu Kaisen - S02E03'),
    { group: 'Judas', rest: 'Jujutsu Kaisen - S02E03' });
});

test('only the leading run is taken, never a later one', () => {
  // `[1080p WEBRip AV1]` at the tail is quality, and the existing junk walk
  // reads it correctly. Taking it here would strip the resolution.
  const out = extractLeadingGroup('[Onalrie] ReZero - S04E18 [1080p WEBRip AV1]');
  assert.equal(out?.group, 'Onalrie');
  assert.equal(out?.rest, 'ReZero - S04E18 [1080p WEBRip AV1]');
});

test('a name with no leading bracket is left exactly alone', () => {
  assert.equal(extractLeadingGroup('The.Matrix.1999.1080p.BluRay.x264-GRP'), null);
  assert.equal(extractLeadingGroup('Ghosts.S05E12.1080p.WEB.h264-GRP'), null);
});

test('a bracketed title is kept when nothing lettered follows it', () => {
  // `[REC]` is a real film, and so are `[REC]2` and `[REC]3`. Stripping the
  // brackets would leave a title of `2007` -- that is, none at all. The rule
  // is that a group has to be followed by something that could be a title.
  assert.equal(extractLeadingGroup('[REC].2007.1080p.BluRay.x264-GRP'), null);
  assert.equal(extractLeadingGroup('[REC] 2 (2009)'), null);
  assert.equal(extractLeadingGroup('[REC]'), null);
});

test('an empty or unclosed bracket is not a group', () => {
  assert.equal(extractLeadingGroup('[] Something S01E01'), null);
  assert.equal(extractLeadingGroup('[Unclosed Something S01E01'), null);
});

test('the reported anime name parses to the show alone', () => {
  const result = parseVideo('tv', '[Onalrie] ReZero kara Hajimeru Isekai Seikatsu - S04E18 [1080p WEBRip AV1]');
  if (!result.ok) throw new Error(`refused: ${result.refusal}`);
  assert.equal(result.parsed.title, 'ReZero kara Hajimeru Isekai Seikatsu');
  assert.equal(result.parsed.group, 'Onalrie', 'the group is recovered, not merely dropped');
  assert.equal(result.parsed.quality.resolution, '1080p');
  assert.equal(result.parsed.kind, 'episode');
  if (result.parsed.kind !== 'episode') return;
  assert.equal(result.parsed.seasonNumber, 4);
  assert.deepEqual(result.parsed.episodeNumbers, [18]);
});

test('the other two bracket styles parse the same way', () => {
  for (const [name, title, group] of [
    ['[SubsPlease] Frieren - S01E12 [1080p].mkv', 'Frieren', 'SubsPlease'],
    ['(Erai-raws) Bleach - S01E05 [1080p][HEVC].mkv', 'Bleach', 'Erai-raws'],
    ['{Judas} Jujutsu Kaisen - S02E03 [1080p].mkv', 'Jujutsu Kaisen', 'Judas'],
  ] as const) {
    const result = parseVideo('tv', name);
    if (!result.ok) throw new Error(`refused: ${name}`);
    assert.equal(result.parsed.title, title, name);
    assert.equal(result.parsed.group, group, name);
  }
});

test('an external id in braces is still an id, not a group', () => {
  // `{tmdb-603}` is brace-delimited too, and the id lift has to win: taking
  // it as a group would throw away an assertion about which record this is.
  const result = parseVideo('movies', '{tmdb-603} The Matrix (1999).mkv');
  if (!result.ok) throw new Error('refused');
  assert.deepEqual(result.parsed.externalId, { source: 'tmdb', id: '603' });
  assert.equal(result.parsed.title, 'The Matrix');
  assert.equal(result.parsed.group, null, 'the id was consumed as an id');
});

test('a trailing group already found is not overwritten by a leading one', () => {
  // Both present is rare and genuinely ambiguous. Preferring the existing
  // reading means this change cannot regress a group that parses correctly
  // today.
  const result = parseVideo('tv', '[Onalrie] ReZero - S04E18 1080p.WEB.h264-FUZEER');
  if (!result.ok) throw new Error('refused');
  assert.equal(result.parsed.group, 'FUZEER');
});

test('the xxx path is deliberately left alone', () => {
  // A scene release writes its *studio* in the leading brackets, and
  // `parseScene` reads that as the site -- the strongest signal it has,
  // since `site_id` plus a date is very nearly a primary key on TPDB.
  // Fifteen corpus names take this shape, and one of them
  // (`[PureTaboo.com] 2020-04-01 - Kenzie Reeves...`) lost its site
  // entirely when the bracket was taken as a group.
  const result = parseVideo('xxx', '[PureTaboo.com] 2020-04-01 - Kenzie Reeves - A Step Too Far [2160p].mp4');
  if (!result.ok) throw new Error('refused');
  assert.equal(result.parsed.kind, 'scene');
  if (result.parsed.kind !== 'scene') return;
  assert.equal(result.parsed.site, 'PureTaboo com', 'the studio is the site, not a group');
  assert.equal(result.parsed.releasedOn, '2020-04-01');
});
