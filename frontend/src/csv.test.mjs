import { test } from 'node:test';
import assert from 'node:assert/strict';
import { rowsToCSV } from './csv.ts';

test('CSV preserves exact values and distinguishes null from an empty string', () => {
  assert.equal(rowsToCSV([['9223372036854775807', '1.0000000000000000001', null, '', '雪']]),
    '9223372036854775807,1.0000000000000000001,,"",雪');
});

test('CSV quotes commas, double quotes and line breaks', () => {
  assert.equal(rowsToCSV([['a,b', 'say "hello"', 'one\ntwo', 'one\rtwo', 'one\r\ntwo']]),
    '"a,b","say ""hello""","one\ntwo","one\rtwo","one\r\ntwo"');
});

test('CSV keeps supplied row and column order without headers or row numbers', () => {
  const rows = [['second', '2'], ['first', '1']];
  assert.equal(rowsToCSV(rows), 'second,2\r\nfirst,1');
  assert.deepEqual(rows, [['second', '2'], ['first', '1']]);
  assert.equal(rowsToCSV([]), '');
});

test('CSV export prepends escaped column names without changing row order or values', () => {
  const rows = [['9223372036854775807', null, ''], ['2', 'one\ntwo', 'quoted"value']];
  const headers = ['id', 'name, full', 'say "hello"'];
  assert.equal(rowsToCSV(rows, headers),
    'id,"name, full","say ""hello"""\r\n9223372036854775807,,""\r\n2,"one\ntwo","quoted""value"');
  assert.deepEqual(headers, ['id', 'name, full', 'say "hello"']);
  assert.equal(rowsToCSV([], ['id', 'note']), 'id,note');
});
