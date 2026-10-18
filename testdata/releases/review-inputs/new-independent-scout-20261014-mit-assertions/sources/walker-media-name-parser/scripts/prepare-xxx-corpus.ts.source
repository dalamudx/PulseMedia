import { readFileSync, writeFileSync } from 'node:fs';

/**
 * Turns the owner-supplied listing into the two fixture files the corpus
 * convention expects. Sync IO on purpose: a one-shot script with nothing
 * else on the event loop.
 *
 * Usenet post subjects are dropped for a mechanical reason. Their part
 * counter `[03/98]` contains a slash, so `splitInput` would read `[03` as a
 * directory name.
 */
const SUBJECT = /-\s*\[\d+\/\d+\]\s*-/;
const SOURCE = '/Users/chad/Projects/media-name-parser/xxx.corpus.txt';

const unique = [...new Set(
  readFileSync(SOURCE, 'utf8').split('\n').map((l) => l.trim()).filter((l) => l.length > 0),
)].sort();
const kept = unique.filter((l) => !SUBJECT.test(l));
const library = kept.filter((l) => l.includes('/'));
const releases = kept.filter((l) => !l.includes('/'));

writeFileSync('fixtures/corpus/xxx.library.raw.txt', `${library.join('\n')}\n`);
writeFileSync('fixtures/corpus/xxx.releases.raw.txt', `${releases.join('\n')}\n`);
console.log(`unique ${unique.length} | kept ${kept.length} | library ${library.length} | releases ${releases.length}`);
