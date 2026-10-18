import { writeFileSync } from 'node:fs';
import { parseVideo } from '../lib/parse/video';
import { splitInput } from '../lib/parse/normalize';
import { findMarker } from '../lib/parse/markers';
import type { Category } from '../lib/parse/types';
import { isJunk, tokenize } from '../lib/parse/tokens';
import { readLines } from './corpus-report';

const PER_SIGNATURE = 3;

/**
 * Lines that broke something during development, pinned so they can never
 * regress silently. Every one of these was a bug at some point: a year fused
 * to a 3D tag, a channel pair mis-associated, a title eaten by a streaming
 * tag, a numeric title refused, a group read out of a Sonarr quality pair.
 * A stratified sample would keep only a few of them by luck.
 */
const PINNED: readonly (readonly [Category, string])[] = [
  ['movies', 'Thor.2011.3D.1080p.BluRay.Half.SBS.DTS.x264-HDMaNiAcS-AsRequested.nzb'],
  ['movies', 'Toy.Story.2.3D.1999.1080p.BluRay.Half.SBS.DTS.x264-HDMaNiAcS.nzb'],
  ['movies', 'Die.Hard.1988.UHD.BluRay.2160p.DTS-HD.MA.5.1.DV.HEVC.HYBRID.REMUX-FraMeSToR.DUAL-LACTATO.nzb'],
  ['movies', 'John.Wick-Chapter.3-Parabellum.[2019].[1080p.BluRay.x265.SDR.DDP.Atmos.7.1.English-DarQ.HONE].nzb'],
  ['movies', 'Mortal.Kombat.II.2026.UHD.BluRay.1080p.DD+Atmos.5.1.DoVi.HDR10+.x265-SM737.nzb'],
  ['movies', 'Outbreak.1995.1080p.BluRay.REMUX.AVC.DTS-HD-MA.5.1-UnKn0wn.nzb'],
  ['movies', 'Outbreak 1995 1080p BluRay REMUX AVC DTS-HD-MA 5 1-UnKn0wn.nzb'],
  ['movies', '360.2012.1080p.AMZN.WEB-DL.DDP5.1.H.264.DUAL-BiOMA.nzb'],
  ['movies', 'IF.2024.NORDiC.1080p.WEB-DL.H.264.DDP5.1.Atmos-NoTrace.nzb'],
  ['movies', 'La.captura.2026.MULTi.VFF.1080p.WEB.EAC3.5.1.x264-FRQC.nzb'],
  // A re-release carries a second year, and `RERELEASE` classified as
  // nothing, so the junk walk stopped on it and swallowed the year, the
  // edition and the first year into the title.
  ['movies', 'Treasure.Island.1999.FULLSCREEN.2003.RERELEASE.NTSC.USA.DVD9-AndreMor'],
  // And once it did not, `Collectors` was the next word to stop the walk,
  // which settled for the 2003 re-release year over the 1998 release.
  ['movies', 'A.Bugs.Life.1998.FULLSCREEN.Collectors.Edition.2003.RERELEASE.DISC1.NTSC.USA.DVD9-AndreMor.nzb'],
  // `Open` and `Matte` were the same shape: neither classified, so the
  // walk stopped on `Matte` and the year stayed in the title.
  ['movies', 'Romeo.Must.Die.2000.Open.Matte.1080p.WEB-DL.HEVC.x265.5.1-BONE'],
  // `Ben.The.Men` is a release group of three bare words. The walk stopped
  // on the first of them, one token short of the `MULTi-Ben` anchor, and
  // kept the year and every tag after it in the title.
  ['movies', 'Movies/Chasing Amy (1997)/Chasing.Amy.1997.1080p.BluRay.REMUX.MULTi-Ben.The.Men.mkv'],
  ['movies', 'The.Mandalorian.And.Grogu.2026.1080p.BluRay.REMUX.MULTi-Ben.The.Men.nzb'],
  // The cost of reaching that anchor: `Dts-HDMa7` reads as one too, and
  // being further left it beats the real `AVC-Pir8`. Title and year go from
  // wrong to right, the group from right to wrong. Pinned so that trade
  // stays visible rather than drifting out of the sample.
  ['movies', 'Movies/Man of Steel (2013)/Man.of.Steel.2013.BluRay.1080p.Dts-HDMa7.1Multi.AVC-Pir8.mkv'],
  // `UHD` named a medium it is not, and the disc it came from was fused to
  // the group, so these reported no frame size and a source of `UHD`.
  ['movies', 'Movies/Evil Dead II (1987)/Evil.Dead.II.1987-COMPLETE.UHD.BLURAY-UNTOUCHED.ISO'],
  ['movies', 'King.Richard.2021.COMPLETE.UHD.BLURAY-B0MBARDiERS-AsRequested.nzb'],
  // A broadcast standard fixes a frame size, and `EUR` -- the region tag
  // beside the `USA` that was already vocabulary -- stopped the walk before
  // the `PAL` behind it could say so.
  ['movies', 'Space.Buddies.2009.WIDESCREEN.PAL.EUR.DVD9-AndreMor.nzb'],
  ['movies', 'Spirit.Untamed.2021.NTSC.USA.DVD5-AndreMor'],
  ['movies', 'Movies/Interstellar (2014)/00136.m2ts'],
  ['movies', 'Movies/Up (2009)/Up.2009.COMPLETE.UHD.BLURAY-AViATOR.iso'],
  ['tv', 'Red River S01E08 1080p CR WEB-DL AAC2.0 H.264-OldT.nzb'],
  ['tv', 'Koln.50667.S2013E015.German.1080p.RTLP.WEB-DL.AAC2.0.H.264-GLOTZE.nzb'],
  ['tv', 'The.Adventures.of.Jimmy.Neutron.Boy.Genius.FULLSCREEN.S03D03.NTSC.USA.DVD9-AndreMor.nzb'],
  ['tv', 'The.Adventures.of.Super.Mario.Bros.3.FULLSCREEN.DISC3.NTSC.USA.DVD5-AndreMor.nzb'],
  ['tv', 'Millionaire.Hot.Seat.AU.S2026.08.25.1080p.WEBDL.h264-P147YPU5.nzb'],
  ['tv', 'Star.Wars.Episode.VI.Return.of.the.Jedi.1983.2160p.UHD.BluRay.REMUX.DV.HDR.HEVC.TrueHD7.1.Atmos-3L.DUAL-LACTATO.nzb'],
  ['tv', 'WWE.Monday.Night.RAW.2026.08.24.Satfeed.720p.HDTV.H264-Star.nzb'],
  ['tv', 'TV Shows/Moon Knight/Season 1/Moon Knight - S01E03 - The Friendly Type Bluray-2160p Remux.mkv'],
  ['tv', 'TV Shows/Star Trek - Prodigy/Season 2/Star Trek - Prodigy - S02E01-E02 - Into the Breach Bluray-1080p Remux.mkv'],
  ['tv', 'TV Shows/Wheel of Fortune/Season 43/Wheel of Fortune - 2026-03-23 - Hawaiian Vacation 1 HDTV-720p.mkv'],
  ['tv', 'TV Shows/Ghosts (US)/Season 5/Ghosts (US) - S05E12 - The List WEBRip-1080p.mkv'],
  ['tv', "TV Shows/The Hitchhiker's Guide to the Galaxy/Specials/The Hitchhiker's Guide to the Galaxy - S00E22 - Recorded at the End of the Universe Bluray-1080p.mkv"],
  ['tv', 'TV Shows/Moon Knight/.plexmatch'],
];

function signature(category: Category, line: string): string {
  const split = splitInput(line);
  const marker = findMarker(split.stem);
  const result = parseVideo(category, line);
  const parts = [
    marker === null ? 'none' : marker.kind,
    marker !== null && 'yearSeason' in marker && marker.yearSeason ? 'yearSeason' : '-',
    split.ancestors.length > 0 ? 'path' : 'flat',
    split.extension ?? 'noext',
    result.ok ? (result.parsed.group === null ? 'nogroup' : 'group') : 'refused',
    result.ok && result.parsed.hints.disambiguator !== null ? 'disamb' : '-',
    result.ok && result.parsed.hints.fromDirectories.length > 0 ? 'dirtitle' : '-',
    // Extra dimensions so the sample actually spans the naming grammars.
    result.ok && result.parsed.year !== null ? 'year' : '-',
    result.ok && result.parsed.kind === 'episode' && result.parsed.episodeNumbers.length > 1 ? 'multiep' : '-',
    result.ok && result.parsed.edition.length > 0 ? 'edition' : '-',
    result.ok && result.parsed.language.length > 0 ? 'lang' : '-',
    result.ok && result.parsed.quality.threeD.length > 0 ? '3d' : '-',
    result.ok && result.parsed.quality.source !== null ? `src:${result.parsed.quality.source.toLowerCase()}` : '-',
    result.ok && tokenize(result.parsed.title).some(isJunk) ? 'leak' : '-',
  ];
  return parts.join('|');
}

function sample(file: string, category: Category): readonly string[] {
  const seen = new Map<string, number>();
  const picked: string[] = [];
  for (const line of readLines(file)) {
    const key = signature(category, line);
    const count = seen.get(key) ?? 0;
    if (count >= PER_SIGNATURE) continue;
    seen.set(key, count + 1);
    picked.push(line);
  }
  return picked;
}

function entry(category: Category, line: string): string {
  const result = parseVideo(category, line);
  const record = result.ok
    ? { name: line, category, expected: result.parsed }
    : { name: line, category, expected: null, expectedRefusal: result.refusal };
  return JSON.stringify(record);
}

function main(): void {
  const groups: readonly {
    readonly out: string;
    readonly files: readonly { readonly file: string; readonly category: Category }[];
  }[] = [
    {
      out: 'fixtures/corpus/movies.golden.jsonl',
      files: [
        { file: 'fixtures/corpus/movies.releases.raw.txt', category: 'movies' },
        { file: 'fixtures/corpus/movies.library.raw.txt', category: 'movies' },
      ],
    },
    {
      out: 'fixtures/corpus/tv.golden.jsonl',
      files: [
        { file: 'fixtures/corpus/tv.releases.raw.txt', category: 'tv' },
        { file: 'fixtures/corpus/tv.library.raw.txt', category: 'tv' },
        { file: 'fixtures/corpus/tv.sport.raw.txt', category: 'tv' },
      ],
    },
  ];
  for (const group of groups) {
    const lines: string[] = [];
    const seenNames = new Set<string>();
    const wanted = new Set(group.files.map((f) => f.category));
    for (const [category, name] of PINNED) {
      if (!wanted.has(category)) continue;
      seenNames.add(name);
      lines.push(entry(category, name));
    }
    for (const { file, category } of group.files) {
      for (const line of sample(file, category)) {
        if (seenNames.has(line)) continue;
        seenNames.add(line);
        lines.push(entry(category, line));
      }
    }
    writeFileSync(group.out, `${lines.join('\n')}\n`);
    console.log(`${group.out}: ${lines.length} entries`);
  }
}

main();
