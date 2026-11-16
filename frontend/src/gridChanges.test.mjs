import { test } from 'node:test';
import assert from 'node:assert/strict';
import { selectRowRange, updateCellEdit, buildRowChanges, applyRowChanges } from './gridChanges.ts';

test('row ranges follow displayed order in either drag direction', () => {
  const order = [4, 1, 7, 0];
  assert.deepEqual(selectRowRange(order, 1, 0), [1, 7, 0]);
  assert.deepEqual(selectRowRange(order, 0, 1), [1, 7, 0]);
  assert.deepEqual(selectRowRange(order, 7, 7), [7]);
  assert.deepEqual(selectRowRange(order, 2, 7), [7]);
  assert.deepEqual(selectRowRange(order, 1, 2), []);
});

test('cell edits distinguish null from empty and clean up changed-back values', () => {
  const first = updateCellEdit({}, 0, 'note', '', null);
  assert.deepEqual(first, { 0: { note: '' } });
  const second = updateCellEdit(first, 0, 'other', null, '');
  assert.deepEqual(second, { 0: { note: '', other: null } });
  const third = updateCellEdit(second, 0, 'note', null, null);
  assert.deepEqual(third, { 0: { other: null } });
  assert.deepEqual(updateCellEdit(third, 0, 'other', '', ''), {});
  assert.deepEqual(first, { 0: { note: '' } });
  assert.deepEqual(second, { 0: { note: '', other: null } });
});

test('cell edits preserve column names that match object prototype properties', () => {
  const edits = updateCellEdit({}, 0, '__proto__', 'changed', 'original');
  assert.equal(Object.hasOwn(edits[0], '__proto__'), true);
  assert.equal(edits[0].__proto__, 'changed');
  assert.deepEqual(updateCellEdit(edits, 0, '__proto__', 'original', 'original'), {});
});

test('save changes use original snapshots and deletion wins over edits without duplicates', () => {
  const rows = [['9223372036854775807', null], ['2', 'original'], ['3', 'kept']];
  const edits = { 0: { id: '9007199254740993', note: '' }, 1: { note: 'edited' }, 2: {} };
  const changes = buildRowChanges(rows, edits, [1, 1, 99]);
  assert.deepEqual(changes, [
    { original: ['9223372036854775807', null], values: { id: '9007199254740993', note: '' }, delete: false },
    { original: ['2', 'original'], values: {}, delete: true },
  ]);
  rows[0][0] = 'different';
  edits[0].note = 'different';
  assert.equal(changes[0].original[0], '9223372036854775807');
  assert.equal(changes[0].values.note, '');
  assert.deepEqual(buildRowChanges(rows, {}, [2]), [{ original: ['3', 'kept'], values: {}, delete: true }]);
  assert.deepEqual(buildRowChanges(rows, {}, []), []);
});

test('saved query edits use source column order and preserve null, empty and exact keys', () => {
  const rows = [['before', '9007199254740993', null], ['removed', '2', 'x'], ['kept', '3', '']];
  const edits = { 0: { note: '', id: '9223372036854775807', ['__proto__']: '' }, 1: { note: 'discarded' } };
  const saved = applyRowChanges(rows, ['note', 'id', '__proto__'], edits, [1]);
  assert.deepEqual(saved, [['', '9223372036854775807', ''], ['kept', '3', '']]);
  assert.deepEqual(rows, [['before', '9007199254740993', null], ['removed', '2', 'x'], ['kept', '3', '']]);
  assert.deepEqual(buildRowChanges(saved, { 0: { note: null } }, []), [
    { original: ['', '9223372036854775807', ''], values: { note: null }, delete: false },
  ]);
  assert.deepEqual(applyRowChanges(rows, ['note', 'id', '__proto__'], {}, []), rows);
  assert.deepEqual(applyRowChanges(rows, ['note', 'id', '__proto__'], {}, [0, 1, 2]), []);
});
