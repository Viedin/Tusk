export type CellValue = string | null;
export type RowEdits = Record<number, Record<string, CellValue>>;

export function selectRowRange(order: number[], anchor: number, target: number): number[] {
  const start = order.indexOf(anchor);
  const end = order.indexOf(target);
  if (end < 0) return [];
  if (start < 0) return [target];
  return order.slice(Math.min(start, end), Math.max(start, end) + 1);
}

export function updateCellEdit(edits: RowEdits, row: number, column: string, value: CellValue, original: CellValue): RowEdits {
  const next = { ...edits };
  const values = { ...next[row], [column]: value };
  if (value === original) delete values[column];
  if (Object.keys(values).length) next[row] = values;
  else delete next[row];
  return next;
}

export function buildRowChanges(rows: CellValue[][], edits: RowEdits, deleted: number[]): { original: CellValue[]; values: Record<string, CellValue>; delete: boolean }[] {
  const removed = new Set(deleted);
  const indices = new Set([...Object.keys(edits).map(Number), ...deleted]);
  return [...indices].filter(index => rows[index] && (removed.has(index) || Object.keys(edits[index] ?? {}).length)).map(index => ({
    original: [...rows[index]],
    values: removed.has(index) ? {} : { ...edits[index] },
    delete: removed.has(index),
  }));
}

export function applyRowChanges(rows: CellValue[][], columns: string[], edits: RowEdits, deleted: number[]): CellValue[][] {
  const removed = new Set(deleted);
  return rows.flatMap((row, index) => removed.has(index) ? [] : [row.map((value, column) =>
    Object.hasOwn(edits[index] ?? {}, columns[column]) ? edits[index][columns[column]] : value)]);
}
