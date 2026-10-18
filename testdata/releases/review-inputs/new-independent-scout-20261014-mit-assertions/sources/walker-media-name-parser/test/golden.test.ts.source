import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { z } from 'zod';
import { parseVideo } from '../lib/parse/video';

const entrySchema = z.object({
  name: z.string(),
  category: z.enum(['tv', 'movies', 'books', 'xxx']),
  // `unknown` here is the structural-comparison exception from Global
  // Constraints: the value is only ever deep-compared against a freshly
  // serialized parse, never read field by field, so giving it a shape would
  // duplicate ParsedVideo for no benefit.
  expected: z.record(z.string(), z.unknown()).nullable(),
  expectedRefusal: z.string().optional(),
});

const FILES = [
  'fixtures/corpus/movies.golden.jsonl',
  'fixtures/corpus/tv.golden.jsonl',
  'fixtures/corpus/xxx.golden.jsonl',
];

for (const file of FILES) {
  test(`golden expectations hold for ${file}`, () => {
    const lines = readFileSync(file, 'utf8').split('\n').filter((l) => l.length > 0);
    assert.ok(lines.length > 0, `${file} is empty`);
    const failures: string[] = [];
    for (const line of lines) {
      const entry = entrySchema.parse(JSON.parse(line));
      const result = parseVideo(entry.category, entry.name);
      if (entry.expected === null) {
        if (result.ok) failures.push(`${entry.name}\n  expected refusal, got a parse`);
        continue;
      }
      if (!result.ok) {
        failures.push(`${entry.name}\n  expected a parse, got refusal: ${result.refusal}`);
        continue;
      }
      const actual = JSON.parse(JSON.stringify(result.parsed)) as unknown;
      try {
        assert.deepEqual(actual, entry.expected);
      } catch {
        failures.push(
          `${entry.name}\n  expected ${JSON.stringify(entry.expected)}\n  actual   ${JSON.stringify(actual)}`,
        );
      }
    }
    assert.equal(failures.length, 0, `${failures.length} golden mismatches:\n\n${failures.join('\n\n')}`);
  });
}
