import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';

const root = new URL('../src/features/configuration/', import.meta.url);
const compile = text => ts.transpileModule(text, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText;
const load = text => import(`data:text/javascript;base64,${Buffer.from(compile(text)).toString('base64')}#${crypto.randomUUID()}`);
const catalog = await load(readFileSync(new URL('discoveryCatalog.ts', root), 'utf8'));

test('isDoubanCategory and doubanCategoryToList accurately map 5 categories', () => {
  const tmdbCats = ['trending', 'movie', 'tv', 'top_rated', 'now_playing'];
  for (const cat of tmdbCats) {
    assert.equal(catalog.isDoubanCategory(cat), false);
  }

  const doubanCats = ['douban_hot_movie', 'douban_hot_tv', 'douban_weekly_chinese', 'douban_weekly_global', 'douban_top250'];
  for (const cat of doubanCats) {
    assert.equal(catalog.isDoubanCategory(cat), true);
  }

  assert.equal(catalog.doubanCategoryToList['douban_hot_movie'], 'hot_movie');
  assert.equal(catalog.doubanCategoryToList['douban_hot_tv'], 'hot_tv');
  assert.equal(catalog.doubanCategoryToList['douban_weekly_chinese'], 'weekly_chinese');
  assert.equal(catalog.doubanCategoryToList['douban_weekly_global'], 'weekly_global');
  assert.equal(catalog.doubanCategoryToList['douban_top250'], 'top250');
});

test('doubanMediaItem maps fields, sets rating_source and formats overview', () => {
  const mapped = catalog.doubanMediaItem({
    douban_id: "1292052",
    tmdb_id: 278,
    title: "肖申克的救赎",
    original_title: "The Shawshank Redemption",
    media_type: "movie",
    year: 1994,
    rating: 9.7,
    poster_url: "https://img3.doubanio.com/p4807472.jpg"
  });

  assert.equal(mapped.douban_id, "1292052");
  assert.equal(mapped.tmdb_id, 278);
  assert.equal(mapped.title, "肖申克的救赎");
  assert.equal(mapped.media_type, "movie");
  assert.equal(mapped.year, 1994);
  assert.equal(mapped.rating, 9.7);
  assert.equal(mapped.rating_source, "douban");
  assert.equal(mapped.overview, "原名：The Shawshank Redemption");
  assert.equal(mapped.poster_url, "https://img3.doubanio.com/p4807472.jpg");

  // Unmatched item leaves tmdb_id as 0
  const unmatched = catalog.doubanMediaItem({
    douban_id: "99999",
    title: "未知片",
    media_type: "tv"
  });
  assert.equal(unmatched.tmdb_id, 0);
  assert.equal(unmatched.rating_source, "douban");
  assert.equal(unmatched.overview, undefined);
});
