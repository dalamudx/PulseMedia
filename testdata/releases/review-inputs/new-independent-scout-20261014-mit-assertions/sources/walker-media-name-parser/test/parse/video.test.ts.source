import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseVideo } from '../../lib/parse/video';

function ok(category: 'tv' | 'movies', input: string) {
  const result = parseVideo(category, input);
  assert.equal(result.ok, true, `refused: ${result.ok ? '' : result.refusal}`);
  if (!result.ok) throw new Error('unreachable');
  return result.parsed;
}

test('a scene movie', () => {
  const got = ok('movies', 'Outbreak.1995.1080p.BluRay.REMUX.AVC.DTS-HD-MA.5.1-UnKn0wn.nzb');
  assert.equal(got.kind, 'movie');
  assert.equal(got.title, 'Outbreak');
  assert.equal(got.year, 1995);
  assert.equal(got.group, 'UnKn0wn');
  assert.equal(got.quality.resolution, '1080p');
  assert.equal(got.quality.source, 'BluRay');
  assert.equal(got.categoryDisagreement, false);
});

test('a contradictory UHD source at 1080p keeps the explicit resolution', () => {
  const got = ok('movies', 'Mortal.Kombat.II.2026.UHD.BluRay.1080p.DD+Atmos.5.1.DoVi.HDR10+.x265-SM737.nzb');
  assert.equal(got.title, 'Mortal Kombat II');
  assert.equal(got.quality.resolution, '1080p');
});

test('a movie whose title and year come only from the parent directory', () => {
  const got = ok('movies', 'Movies/Interstellar (2014)/00136.m2ts');
  assert.equal(got.kind, 'movie');
  assert.equal(got.title, 'Interstellar');
  assert.equal(got.year, 2014);
  assert.deepEqual(got.hints.fromDirectories, ['Interstellar (2014)']);
});

test('a library episode with a Sonarr quality suffix', () => {
  const got = ok('tv', 'TV Shows/Moon Knight/Season 1/Moon Knight - S01E03 - The Friendly Type Bluray-2160p Remux.mkv');
  assert.equal(got.kind, 'episode');
  assert.equal(got.title, 'Moon Knight');
  if (got.kind !== 'episode') throw new Error('unreachable');
  assert.equal(got.seasonNumber, 1);
  assert.deepEqual(got.episodeNumbers, [3]);
  assert.equal(got.episodeTitle, 'The Friendly Type');
  assert.equal(got.quality.resolution, '2160p');
});

test('a series title containing the field separator survives', () => {
  const got = ok('tv', 'TV Shows/Star Trek - Prodigy/Season 2/Star Trek - Prodigy - S02E01-E02 - Into the Breach Bluray-1080p Remux.mkv');
  assert.equal(got.title, 'Star Trek Prodigy');
  if (got.kind !== 'episode') throw new Error('unreachable');
  assert.deepEqual(got.episodeNumbers, [1, 2]);
  assert.equal(got.episodeTitle, 'Into the Breach');
});

test('a directory disambiguator is captured, not swallowed into the title', () => {
  const got = ok('tv', 'TV Shows/Ghosts (US)/Season 5/Ghosts (US) - S05E12 - The List WEBRip-1080p.mkv');
  assert.equal(got.title, 'Ghosts');
  assert.equal(got.hints.disambiguator, 'US');
});

test('the other Ghosts is distinguished by its year disambiguator', () => {
  const got = ok('tv', 'TV Shows/Ghosts (2019)/Season 1/Ghosts (2019) - S01E01 - Pilot WEBDL-1080p.mkv');
  assert.equal(got.title, 'Ghosts');
  assert.equal(got.hints.disambiguator, '2019');
});

test('a special is season zero', () => {
  const got = ok('tv', "TV Shows/The Hitchhiker's Guide to the Galaxy/Specials/The Hitchhiker's Guide to the Galaxy - S00E22 - Recorded at the End of the Universe Bluray-1080p.mkv");
  if (got.kind !== 'episode') throw new Error('unreachable');
  assert.equal(got.seasonNumber, 0);
  assert.deepEqual(got.episodeNumbers, [22]);
});

test('a date-based episode takes its season from the directory', () => {
  const got = ok('tv', 'TV Shows/Wheel of Fortune/Season 43/Wheel of Fortune - 2026-03-23 - Hawaiian Vacation 1 HDTV-720p.mkv');
  assert.equal(got.title, 'Wheel of Fortune');
  if (got.kind !== 'episode') throw new Error('unreachable');
  assert.equal(got.airDate, '2026-03-23');
  assert.equal(got.seasonNumber, 43);
});

test('a year-season is flagged rather than treated as season 2013', () => {
  const got = ok('tv', 'Koln.50667.S2013E015.German.1080p.RTLP.WEB-DL.AAC2.0.H.264-GLOTZE.nzb');
  if (got.kind !== 'episode') throw new Error('unreachable');
  assert.equal(got.seasonNumber, 2013);
  assert.equal(got.yearSeason, true);
  assert.equal(got.title, 'Koln 50667');
});

test('a DVD disc is a season with a disc hint, never an episode', () => {
  const got = ok('tv', 'The.Adventures.of.Jimmy.Neutron.Boy.Genius.FULLSCREEN.S03D03.NTSC.USA.DVD9-AndreMor.nzb');
  assert.equal(got.kind, 'season');
  if (got.kind !== 'season') throw new Error('unreachable');
  assert.equal(got.seasonNumber, 3);
  assert.equal(got.hints.discNumber, 3);
});

test('the caller category wins: a movie list entry with an episode marker', () => {
  const got = ok('movies', 'Some.Show.S02E04.1080p.WEB-DL-GRP.nzb');
  assert.equal(got.kind, 'movie');
  assert.equal(got.categoryDisagreement, true);
});

test('the caller category wins the other way too', () => {
  const got = ok('tv', 'Star.Wars.Episode.VI.Return.of.the.Jedi.1983.2160p.UHD.BluRay.REMUX.DV.HDR.HEVC.TrueHD7.1.Atmos-3L.DUAL-LACTATO.nzb');
  assert.equal(got.kind, 'series');
  assert.equal(got.title, 'Star Wars Episode VI Return of the Jedi');
  assert.equal(got.categoryDisagreement, true);
  assert.equal(got.group, '3L');
});

test('a Plex sidecar is refused, not parsed', () => {
  const result = parseVideo('tv', 'TV Shows/Moon Knight/.plexmatch');
  assert.equal(result.ok, false);
  if (result.ok) throw new Error('unreachable');
  assert.match(result.refusal, /not a media file/);
});

test('a refusal quotes the extension, so an invisible character is visible', () => {
  // The quoting exists because the extension is untrusted text that may carry
  // a character with no width, and interpolated bare it produced `sidecar
  // .srt` -- indistinguishable from a clean one.
  //
  // This used to be asserted through an unknown extension, which no longer
  // refuses anything: an unrecognised trailing segment is now left in the name
  // rather than treated as a bad extension. A recognised sidecar is the case
  // that still refuses, so it is the case that still needs the quoting.
  const result = parseVideo('tv', 'Moon Knight - S01E01.srt');
  assert.equal(result.ok, false);
  if (result.ok) throw new Error('unreachable');
  assert.ok(result.refusal.includes('".srt"'),
    `the extension must be quoted: ${result.refusal}`);
});

test('an unrecognised trailing segment stays in the name rather than refusing', () => {
  // The cost of making the extension optional, recorded rather than hidden.
  // `Something.2020.mkv` with a zero-width space after `mkv` is not a known
  // extension, so nothing is stripped and the stray token lands in the group.
  // Garbage in, garbage adjacent -- but the title and year are still right,
  // which is what a caller came for.
  const result = parseVideo('movies', 'Something.2020.mkv\u200B');
  assert.ok(result.ok);
  if (!result.ok) throw new Error('unreachable');
  assert.equal(result.parsed.title, 'Something');
  assert.equal(result.parsed.year, 2020);
});

test('a subtitle is refused', () => {
  const result = parseVideo('tv', 'TV Shows/Moon Knight/Season 1/Moon Knight - S01E01.srt');
  assert.equal(result.ok, false);
});

test('a name with no title left after junk removal is refused', () => {
  const result = parseVideo('movies', '1080p.BluRay.x264-GRP.mkv');
  assert.equal(result.ok, false);
  if (result.ok) throw new Error('unreachable');
  assert.match(result.refusal, /no title/);
});

// --- the extension is optional -------------------------------------------

test('a name with no extension parses, group and all', () => {
  // A caller may hold nothing but a name -- another service handing over
  // something it never had a file for -- and requiring an extension was an
  // arbitrary gate. The group is the part at risk: an unrecognised trailing
  // segment must not be stripped on the assumption that it is an extension.
  const result = parseVideo('movies', 'The.Matrix.1999.1080p.BluRay.x264-GRP');
  assert.ok(result.ok);
  if (!result.ok) throw new Error('unreachable');
  assert.equal(result.parsed.title, 'The Matrix');
  assert.equal(result.parsed.year, 1999);
  assert.equal(result.parsed.group, 'GRP', 'the group must survive having no extension');
});

test('a bare title parses', () => {
  const result = parseVideo('movies', 'Some Movie Name');
  assert.ok(result.ok);
  if (!result.ok) throw new Error('unreachable');
  assert.equal(result.parsed.title, 'Some Movie Name');
});

test('a trailing number is not mistaken for an extension', () => {
  const result = parseVideo('movies', 'Movie.Part.2');
  assert.ok(result.ok);
  if (!result.ok) throw new Error('unreachable');
  assert.equal(result.parsed.title, 'Movie Part 2');
});

test('a known extension still splits exactly as before', () => {
  // The property that makes the loosening safe: a recognised extension is
  // stripped as it always was, so the stem -- and therefore the
  // `normalized_key` of every already-cached name -- is untouched.
  const withExt = parseVideo('movies', 'The.Matrix.1999.1080p.BluRay.x264-GRP.mkv');
  const without = parseVideo('movies', 'The.Matrix.1999.1080p.BluRay.x264-GRP');
  assert.ok(withExt.ok && without.ok);
  if (!withExt.ok || !without.ok) throw new Error('unreachable');
  assert.deepEqual(withExt.parsed, without.parsed,
    'with and without the extension must parse identically');
});

test('a sidecar and a dotfile are still refused', () => {
  // Loosening the requirement is not removing it. An absent extension says
  // nothing, but `.srt` and a leading-dot name each say the thing named is
  // *about* media rather than media, and a clean refusal is the useful answer.
  for (const [name, pattern] of [
    ['TV Shows/Moon Knight/Season 1/Moon Knight - S01E01.srt', /sidecar/],
    ['TV Shows/Moon Knight/.plexmatch', /dotfile/],
    ['Movies/Poster.jpg', /sidecar/],
  ] as const) {
    const result = parseVideo('tv', name);
    assert.equal(result.ok, false, name);
    if (result.ok) throw new Error('unreachable');
    assert.match(result.refusal, pattern, name);
  }
});

test('a torrent is stripped like an nzb, being the same kind of metafile', () => {
  const result = parseVideo('movies', 'Movie.2020.torrent');
  assert.ok(result.ok);
  if (!result.ok) throw new Error('unreachable');
  assert.equal(result.parsed.title, 'Movie');
  assert.equal(result.parsed.year, 2020);
  assert.ok(!/torrent/i.test(result.parsed.title), 'it must not reach the title');
});

test('a re-released disc keeps its title, its year and its edition', () => {
  const result = parseVideo('movies', 'Treasure.Island.1999.FULLSCREEN.2003.RERELEASE.NTSC.USA.DVD9-AndreMor');
  if (!result.ok) throw new Error(`refused: ${result.refusal}`);
  assert.equal(result.parsed.title, 'Treasure Island');
  assert.equal(result.parsed.year, 1999, 'the first year, not the re-release year');
  assert.deepEqual(result.parsed.edition, ['FULLSCREEN', 'RERELEASE']);
  assert.equal(result.parsed.group, 'AndreMor');
});

test('an open matte transfer keeps its title and its year', () => {
  const result = parseVideo('movies', 'Romeo.Must.Die.2000.Open.Matte.1080p.WEB-DL.HEVC.x265.5.1-BONE');
  if (!result.ok) throw new Error(`refused: ${result.refusal}`);
  assert.equal(result.parsed.title, 'Romeo Must Die');
  assert.equal(result.parsed.year, 2000);
  assert.deepEqual(result.parsed.edition, ['OpenMatte']);
  assert.equal(result.parsed.group, 'BONE');
});

test('a release group named in several words keeps the title and the year', () => {
  const result = parseVideo('movies', 'The.Informer.2019.1080p.BluRay.REMUX.MULTi-Ben.The.Men');
  if (!result.ok) throw new Error(`refused: ${result.refusal}`);
  assert.equal(result.parsed.title, 'The Informer');
  assert.equal(result.parsed.year, 2019);
  assert.equal(result.parsed.group, 'Ben.The.Men');
});

test('a UHD BluRay rip reports the disc as its source and its real frame size', () => {
  // `UHD` named the medium, so the medium it was actually made from was
  // dropped and the frame size went unreported.
  const got = ok('movies', 'Movies/Evil Dead II (1987)/Evil.Dead.II.1987-COMPLETE.UHD.BLURAY.iso');
  assert.equal(got.quality.source, 'BLURAY');
  assert.equal(got.quality.resolution, '2160p');
});

test('an explicit frame size beats the disc it was made from', () => {
  // A 1080p encode off a UHD BluRay is 1080p. Ten corpus names have this
  // shape, and order must not decide it: `UHD` comes first.
  const got = ok('movies', 'Mortal.Kombat.II.2026.UHD.BluRay.1080p.DD+Atmos.5.1.DoVi.HDR10+.x265-SM737.nzb');
  assert.equal(got.quality.resolution, '1080p');
  assert.equal(got.quality.source, 'BluRay');
});

test('a medium fused to a group name is still the medium', () => {
  // `BLURAY-UNTOUCHED` classifies as nothing -- its group half is not
  // vocabulary -- so the disc went unreported and `UHD` was the only thing
  // supplying a source. Once `UHD` became a resolution, 72 corpus names of
  // this shape had no source at all, though the medium is right there in
  // the name. `splitGroupSuffix` already separates the two.
  const got = ok('movies', 'Movies/Evil Dead II (1987)/Evil.Dead.II.1987-COMPLETE.UHD.BLURAY-UNTOUCHED.ISO');
  assert.equal(got.quality.source, 'BLURAY');
  assert.equal(got.quality.resolution, '2160p');
  assert.equal(got.group, 'UNTOUCHED');
});

test('a PAL disc reports the frame size the standard fixes', () => {
  const got = ok('movies', 'Speed.Racer.2008.PAL.GER.DVD9-AndreMor');
  assert.equal(got.quality.resolution, '576i');
  assert.equal(got.quality.source, 'DVD9');
  assert.equal(got.title, 'Speed Racer');
  assert.equal(got.year, 2008);
});

test('an NTSC disc reports its own standard, not the other one', () => {
  const got = ok('movies', 'Spirit.Untamed.2021.NTSC.USA.DVD5-AndreMor');
  assert.equal(got.quality.resolution, '480i');
  assert.equal(got.quality.source, 'DVD5');
  assert.equal(got.title, 'Spirit Untamed');
  assert.equal(got.year, 2021);
});

test('a European region tag does not stop the walk where the American one does not', () => {
  // `USA` was ancillary vocabulary and `EUR` was nothing, so the walk
  // stopped on it and kept `PAL`, the edition and the year in the title --
  // which also meant the PAL standard never reached extraction.
  const got = ok('movies', 'Space.Buddies.2009.WIDESCREEN.PAL.EUR.DVD9-AndreMor.nzb');
  assert.equal(got.title, 'Space Buddies');
  assert.equal(got.year, 2009);
  assert.equal(got.quality.resolution, '576i');
  assert.equal(got.quality.source, 'DVD9');
  assert.deepEqual(got.edition, ['WIDESCREEN']);
});
