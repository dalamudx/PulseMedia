import { existsSync, readFileSync, writeFileSync } from 'node:fs';

const BASELINE = 'fixtures/corpus/baseline.json';
import { parseVideo } from '../lib/parse/video';
import { isJunk, tokenize } from '../lib/parse/tokens';
import type { Category } from '../lib/parse/types';

export interface CorpusRate {
  readonly file: string;
  readonly total: number;
  readonly parsed: number;
  readonly refused: number;
  /** Parsed, but with an empty title. A parser bug, not an honest refusal. */
  readonly failed: number;
  /** Threw. Always unacceptable — the parser must never throw on any input. */
  readonly threw: number;
  readonly rate: number;
  /**
   * Titles that still contain a vocabulary token — quality junk that leaked
   * past the boundary walk.
   *
   * This is the metric that carries real information. `rate` is close to
   * tautological: it only falls when the parser throws or produces an empty
   * title, so a parser that returned the whole filename as the title would
   * still score 100%. A leak, by contrast, means the title is definitely
   * wrong. It over-counts slightly — a Grand Prix name legitimately contains
   * `Dutch`, which is also a language tag — so it is a ceiling to defend, not
   * a number to drive to zero.
   */
  readonly titleLeaks: number;
}

export interface CorpusFile {
  readonly file: string;
  readonly category: Category;
}

export const CORPUS: readonly CorpusFile[] = [
  { file: 'fixtures/corpus/movies.releases.raw.txt', category: 'movies' },
  { file: 'fixtures/corpus/movies.library.raw.txt', category: 'movies' },
  { file: 'fixtures/corpus/tv.releases.raw.txt', category: 'tv' },
  { file: 'fixtures/corpus/tv.library.raw.txt', category: 'tv' },
  { file: 'fixtures/corpus/tv.sport.raw.txt', category: 'tv' },
  { file: 'fixtures/corpus/xxx.releases.raw.txt', category: 'xxx' },
  { file: 'fixtures/corpus/xxx.library.raw.txt', category: 'xxx' },
];

export function readLines(file: string): readonly string[] {
  return readFileSync(file, 'utf8').split('\n').filter((line) => line.length > 0);
}

export function measure(file: string, category: Category): CorpusRate {
  const lines = readLines(file);
  let parsed = 0;
  let refused = 0;
  let failed = 0;
  let threw = 0;
  let titleLeaks = 0;
  for (const line of lines) {
    try {
      const result = parseVideo(category, line);
      if (result.ok) {
        if (result.parsed.title.length > 0) {
          parsed += 1;
          if (tokenize(result.parsed.title).some(isJunk)) titleLeaks += 1;
        } else if (
          category === 'xxx' &&
          result.parsed.kind === 'scene' &&
          result.parsed.site !== null &&
          result.parsed.releasedOn !== null
        ) {
          // A scene's identity is its site + release date, not its title --
          // theporndb matches on `site_id` + `date` and that is enough to
          // find exactly one scene (verified live: site_id=4347&date=
          // 2022-07-07). An empty title here is not a parser failure the way
          // it is for movies/tv, which cannot be looked up without a name.
          parsed += 1;
        } else {
          failed += 1;
        }
      } else {
        refused += 1;
      }
    } catch {
      threw += 1;
    }
  }
  const total = lines.length;
  return {
    file, total, parsed, refused, failed, threw, titleLeaks,
    rate: total === 0 ? 0 : (parsed + refused) / total,
  };
}

/**
 * How many sampled lines reach a provider match.
 *
 * Opt-in via `--resolve` because it needs a database and fixtures, unlike the
 * parse rate which is pure. Sampled rather than exhaustive: running all 5951
 * lines against recorded fixtures would mostly measure fixture coverage, not
 * resolution quality.
 */
export interface ResolveRate {
  readonly file: string;
  readonly sampled: number;
  /** Reached a provider match at or above the confidence floor. */
  readonly resolved: number;
  /** Ran, but the provider found nothing good enough. A real outcome. */
  readonly unmatched: number;
  /** No recorded fixture, so nothing could be measured. Not a failure. */
  readonly unmeasurable: number;
  /** resolved / (sampled - unmeasurable). Null when nothing was measurable. */
  readonly rate: number | null;
}

/**
 * How many *measurable* sampled lines reach a provider match.
 *
 * `unmeasurable` is separated out and excluded from the rate, and that
 * distinction is the whole point. Fixtures exist for a handful of specific
 * titles, so most arbitrary corpus lines have no recording. Counting those as
 * unresolved produces a rate near zero that looks like a resolution failure
 * and is really a statement about how many fixtures have been recorded --
 * which would be a number that lies.
 */
export async function measureResolve(
  file: string,
  category: Category,
  sampleSize: number,
  run: (category: Category, name: string) => Promise<{
    readonly state: string;
    readonly refusal: string | null;
    readonly cached: boolean;
  }>,
  reset?: (names: readonly string[]) => Promise<void>,
): Promise<ResolveRate> {
  const lines = readLines(file);
  const step = Math.max(1, Math.floor(lines.length / sampleSize));

  // Clear any cached verdict for the lines about to be measured.
  //
  // Without this the measurement poisons itself: the first run writes a
  // `pending` row for every line, and every later run hits the cooling path,
  // returns `cached` without calling the provider, and reports a rate that
  // describes the previous run rather than the current code.
  const chosen: string[] = [];
  for (let i = 0; i < lines.length; i += step) {
    const line = lines[i];
    if (line !== undefined) chosen.push(line);
  }
  if (reset !== undefined) await reset(chosen);

  let sampled = 0;
  let resolved = 0;
  let unmatched = 0;
  let unmeasurable = 0;
  for (const line of chosen) {
    sampled += 1;
    // The pipeline catches provider errors and reports them as `pending` with
    // the message on `refusal`, so a fixture miss arrives as a result rather
    // than a throw. Both paths are checked, and a `cached` answer means the
    // reset did not take -- it is not evidence about the provider either way.
    try {
      const result = await run(category, line);
      if ((result.refusal ?? '').includes('no TMDB fixture')) unmeasurable += 1;
      else if (result.cached) unmeasurable += 1;
      else if (result.state === 'resolved') resolved += 1;
      else unmatched += 1;
    } catch (error) {
      if (error instanceof Error && error.message.includes('no TMDB fixture')) unmeasurable += 1;
      else unmatched += 1;
    }
  }
  const measurable = sampled - unmeasurable;
  return {
    file, sampled, resolved, unmatched, unmeasurable,
    rate: measurable === 0 ? null : resolved / measurable,
  };
}

async function main(): Promise<void> {
  const write = process.argv.includes('--write');
  const wantResolve = process.argv.includes('--resolve');
  const rates = CORPUS.map(({ file, category }) => measure(file, category));
  for (const r of rates) {
    const pct = (r.rate * 100).toFixed(2);
    console.log(
      `${r.file.padEnd(42)} total=${String(r.total).padStart(5)} ` +
      `parsed=${String(r.parsed).padStart(5)} refused=${String(r.refused).padStart(4)} ` +
      `failed=${String(r.failed).padStart(4)} threw=${String(r.threw).padStart(4)} ` +
      `leaks=${String(r.titleLeaks).padStart(4)} rate=${pct}%`,
    );
  }
  let resolveRate: Record<string, number> | null = null;
  if (wantResolve) {
    // Imported here, not at the top: the parse-rate path must stay usable with
    // no database and no TMDB fixtures at all.
    const { resolveLookup } = await import('../lib/resolve/pipeline');
    const { createTmdbClient } = await import('../lib/providers/tmdb/client');
    const { createTmdbProvider } = await import('../lib/providers/tmdb/resolve');
    const { fixtureFetch } = await import('../test/support/tmdb-fixtures');
    const { closeDb, withTransaction } = await import('../lib/db/client');
    const { inArray } = await import('drizzle-orm');
    const { lookups } = await import('../lib/db/schema');

    const provider = createTmdbProvider(createTmdbClient({
      token: 'fixture', fetchImpl: fixtureFetch(), ratePerSecond: 1000,
    }));
    const deps = { providers: [provider], now: (): Date => new Date() };
    const sampleSize = 20;
    resolveRate = {};
    console.log('\nResolve rate is over *measurable* lines only. A line with no recorded');
    console.log('fixture is counted as nofixture and excluded, because including it would');
    console.log('report fixture coverage while calling it resolution quality.\n');
    for (const { file, category } of CORPUS) {
      const measured = await measureResolve(
        file, category, sampleSize,
        async (c, name) => resolveLookup({ category: c, name }, deps),
        async (names) => {
          // The query builder rather than raw SQL: drizzle does not expand a
          // JS array into a Postgres array inside a template literal, so both
          // `= ANY($1)` and `= ANY($1::text[])` are rejected by the server.
          await withTransaction(async (tx) => {
            await tx.delete(lookups).where(inArray(lookups.name, [...names]));
          });
        },
      );
      if (measured.rate !== null) resolveRate[file] = Number(measured.rate.toFixed(4));
      console.log(
        `${file.padEnd(42)} sampled=${String(measured.sampled).padStart(4)} ` +
        `resolved=${String(measured.resolved).padStart(4)} ` +
        `unmatched=${String(measured.unmatched).padStart(4)} ` +
        `nofixture=${String(measured.unmeasurable).padStart(4)} ` +
        `rate=${measured.rate === null ? 'n/a' : `${(measured.rate * 100).toFixed(2)}%`}`,
      );
    }
    await closeDb();
  }

  if (write) {
    // `resolveRate` is only measured under `--resolve`, and measuring it costs
    // thousands of live provider calls. A `--write` without it must therefore
    // carry the previous numbers forward rather than replace them with
    // nothing: writing the parse metrics used to silently erase the expensive
    // ones, which is a bad trade to make by accident.
    const previous = existsSync(BASELINE)
      ? (JSON.parse(readFileSync(BASELINE, 'utf8')) as { readonly resolveRate?: unknown })
      : {};
    const carried = wantResolve ? resolveRate : previous.resolveRate ?? resolveRate;

    const baseline = {
      parseRate: Object.fromEntries(rates.map((r) => [r.file, Number(r.rate.toFixed(4))])),
      // Ceilings, not floors: these may go down but must never go up.
      titleLeaks: Object.fromEntries(rates.map((r) => [r.file, r.titleLeaks])),
      // Without this, `rate` cannot detect a parser that refuses everything:
      // refusals count toward the rate, so refuse-all scores 100%.
      refused: Object.fromEntries(rates.map((r) => [r.file, r.refused])),
      resolveRate: carried,
    };
    writeFileSync(BASELINE, `${JSON.stringify(baseline, null, 2)}\n`);
    console.log('\nwrote fixtures/corpus/baseline.json');
  }
}

if (process.argv[1]?.endsWith('corpus-report.ts') === true) {
  await main();
}
