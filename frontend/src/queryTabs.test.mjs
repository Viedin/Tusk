import { test } from 'node:test';
import assert from 'node:assert/strict';
import { loadQueryTabs, saveQueryTabs } from './queryTabs.ts';

const key = 'tusk.query-tabs.v1';

test('open SQL tabs survive a storage round trip', () => {
  const values = new Map();
  const storage = {
    getItem: name => values.get(name) ?? null,
    setItem: (name, value) => values.set(name, value),
  };
  const texts = ['', '  SELECT 1;\n', 'SELECT \'雪, "hello"\';\r\n-- second line', '   '];
  saveQueryTabs(storage, texts, 2);
  const expected = { texts: [...texts], activeIndex: 2 };
  assert.deepEqual(JSON.parse(values.get(key)), expected);
  texts[1] = 'changed after saving';
  assert.deepEqual(loadQueryTabs(storage), expected);
  assert.equal(values.size, 1);
});

test('missing query storage starts without restored tabs', () => {
  assert.equal(loadQueryTabs({ getItem: name => {
    assert.equal(name, key);
    return null;
  } }), null);
});

test('a single empty SQL tab is restored', () => {
  assert.deepEqual(loadQueryTabs({ getItem: () => JSON.stringify({ texts: [''], activeIndex: 0 }) }),
    { texts: [''], activeIndex: 0 });
});

test('invalid saved queries are rejected without overwriting storage', () => {
  for (const raw of ['', '{', 'null', '[]', '42', '"SELECT 1"', '{}',
    '{"texts":[]}', '{"texts":"SELECT 1"}', '{"texts":["SELECT 1",null]}',
    '{"texts":[1]}', '{"texts":[{}]}', '{"texts":[true]}']) {
    let stored = raw;
    let writes = 0;
    const storage = { getItem: () => stored, setItem: (_name, value) => { writes++; stored = value; } };
    assert.throws(() => loadQueryTabs(storage), `Expected rejection for ${raw}`);
    assert.equal(stored, raw);
    assert.equal(writes, 0);
  }
});

test('invalid or missing selected indices use the first tab without losing SQL', () => {
  const texts = ['SELECT 1', 'SELECT 2'];
  for (const activeIndex of [undefined, -1, 0.5, 2, 999, '1', null, true]) {
    assert.deepEqual(loadQueryTabs({ getItem: () => JSON.stringify({ texts, activeIndex }) }),
      { texts, activeIndex: 0 });
  }
  for (const activeIndex of [0, 1]) {
    assert.deepEqual(loadQueryTabs({ getItem: () => JSON.stringify({ texts, activeIndex }) }),
      { texts, activeIndex });
  }
});

test('storage read failures reach the caller', () => {
  const failure = new Error('Storage unavailable');
  assert.throws(() => loadQueryTabs({ getItem: () => { throw failure; } }), error => error === failure);
});

test('storage write failures reach the caller and retain the previous saved tabs', () => {
  const previous = JSON.stringify({ texts: ['SELECT \'kept\''], activeIndex: 0 });
  const values = new Map([[key, previous]]);
  const failure = new Error('Storage quota exceeded');
  const storage = { getItem: name => values.get(name) ?? null, setItem: () => { throw failure; } };
  assert.throws(() => saveQueryTabs(storage, ['SELECT \'new\''], 0), error => error === failure);
  assert.equal(values.get(key), previous);
  assert.deepEqual(loadQueryTabs(storage), { texts: ['SELECT \'kept\''], activeIndex: 0 });
});
