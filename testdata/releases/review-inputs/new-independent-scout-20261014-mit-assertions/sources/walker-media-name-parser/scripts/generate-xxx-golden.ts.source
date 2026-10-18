import { writeFileSync } from 'node:fs';
import { parseVideo } from '../lib/parse/video';
import type { Category } from '../lib/parse/types';
import { readLines } from './corpus-report';

/**
 * Unlike `golden-generate.ts`'s stratified sample for movies/tv, this covers
 * every line in both xxx corpus files. The scene grammar is new (Task 3) and
 * has no history of hand-curated pinned regressions to lean on, and the xxx
 * corpus is exactly the regression surface this task exists to freeze -- a
 * sample would leave most of it unprotected. `fixtures/corpus/xxx.golden.jsonl`
 * is large as a result; that is expected.
 *
 * Sync IO on purpose: a one-shot script that writes one file and exits, with
 * nothing else on the event loop to block.
 */
const FILES: readonly { readonly file: string; readonly category: Category }[] = [
  { file: 'fixtures/corpus/xxx.releases.raw.txt', category: 'xxx' },
  { file: 'fixtures/corpus/xxx.library.raw.txt', category: 'xxx' },
];

const OUT = 'fixtures/corpus/xxx.golden.jsonl';

function entry(category: Category, name: string): string {
  const result = parseVideo(category, name);
  const record = result.ok
    ? { name, category, expected: JSON.parse(JSON.stringify(result.parsed)) as unknown }
    : { name, category, expected: null, expectedRefusal: result.refusal };
  return JSON.stringify(record);
}

function main(): void {
  const lines: string[] = [];
  const seen = new Set<string>();
  for (const { file, category } of FILES) {
    for (const name of readLines(file)) {
      if (seen.has(name)) continue;
      seen.add(name);
      lines.push(entry(category, name));
    }
  }
  writeFileSync(OUT, `${lines.join('\n')}\n`);
  console.log(`${OUT}: ${lines.length} entries`);
}

main();
