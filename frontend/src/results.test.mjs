import { test } from 'node:test';
import assert from 'node:assert/strict';
import { sortedRowIndices, hasAffectedRows, resultStatus } from './results.ts';

test('numeric sorting preserves integers beyond JavaScript precision and decimal scale', () => {
  const rows = [['9007199254740993'], ['9007199254740992'], ['1.0000000000000000002'], ['1.0000000000000000001'], ['-2'], ['-10']];
  assert.deepEqual(sortedRowIndices(rows, 0, 'numeric', false), [5, 4, 3, 2, 1, 0]);
  assert.deepEqual(sortedRowIndices(rows, 0, 'int8', true), [0, 1, 2, 3, 4, 5]);
});

test('numeric sorting handles exponents, negative zero, equal values, infinities and NaN', () => {
  const rows = [['1e1000'], ['9e999'], ['-0.00'], ['0'], ['0.001'], ['-0.001'], ['1.20'], ['1.2'], ['NaN'], ['Infinity'], ['-Infinity']];
  assert.deepEqual(sortedRowIndices(rows, 0, 'numeric', false), [10, 5, 2, 3, 4, 6, 7, 1, 0, 9, 8]);
});

test('text and null sorting is stable, nulls stay last and source rows are not mutated', () => {
  const rows = [['b'], [null], [''], ['a'], ['a'], [null]];
  assert.deepEqual(sortedRowIndices(rows, 0, 'text', false), [2, 3, 4, 0, 1, 5]);
  assert.deepEqual(sortedRowIndices(rows, 0, 'text', true), [0, 3, 4, 2, 1, 5]);
  assert.deepEqual(rows, [['b'], [null], [''], ['a'], ['a'], [null]]);
});

test('affected counts are shown only for modifying commands, including zero-row writes', () => {
  for (const tag of ['SELECT 10', 'CREATE TABLE', 'EXPLAIN', 'FETCH 3', 'COPY 4', '']) assert.equal(hasAffectedRows(tag), false);
  for (const tag of ['INSERT 0 1', 'UPDATE 0', 'DELETE 2', 'MERGE 3']) assert.equal(hasAffectedRows(tag), true);
  assert.equal(resultStatus({ columns: [{}], rowCount: 3, rowsAffected: '3', commandTag: 'SELECT 3' }), '3 rows returned');
  assert.equal(resultStatus({ columns: [{}], rowCount: 1000, rowsAffected: '1000000', commandTag: 'SELECT 1000000' }), '1000 rows returned');
  assert.equal(resultStatus({ columns: [], rowCount: 0, rowsAffected: '0', commandTag: 'UPDATE 0' }), '0 rows affected');
  assert.equal(resultStatus({ columns: [{}], rowCount: 1, rowsAffected: '1', commandTag: 'DELETE 1' }), '1 rows returned · 1 rows affected');
  assert.equal(resultStatus({ columns: [], rowCount: 0, rowsAffected: '0', commandTag: 'CREATE TABLE' }), 'CREATE TABLE completed');
});
