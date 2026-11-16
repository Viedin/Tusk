export type CellValue = string | null;

type NumericKey = { rank: number; negative: boolean; digits: string; power: bigint };

function numericKey(value: string): NumericKey | null {
  const special = value.toLowerCase();
  if (['-infinity', 'infinity', '+infinity', 'nan'].includes(special)) {
    return { rank: special === '-infinity' ? 0 : special === 'nan' ? 3 : 2, negative: false, digits: '', power: 0n };
  }
  const parts = /^([+-]?)(?:(\d+)(?:\.(\d*))?|\.(\d+))(?:[eE]([+-]?\d+))?$/.exec(value);
  if (!parts) return null;
  const fraction = parts[3] ?? parts[4] ?? '';
  const significant = `${parts[2] ?? ''}${fraction}`.replace(/^0+/, '');
  if (!significant) return { rank: 1, negative: false, digits: '0', power: 0n };
  const digits = significant.replace(/0+$/, '');
  return {
    rank: 1,
    negative: parts[1] === '-',
    digits,
    power: BigInt(parts[5] ?? 0) - BigInt(fraction.length) + BigInt(significant.length - digits.length),
  };
}

function compareNumbers(left: NumericKey, right: NumericKey): number {
  if (left.rank !== right.rank) return left.rank < right.rank ? -1 : 1;
  if (left.rank !== 1) return 0;
  if (left.negative !== right.negative) return left.negative ? -1 : 1;
  const direction = left.negative ? -1 : 1;
  if (left.digits === '0' || right.digits === '0') {
    return left.digits === right.digits ? 0 : (left.digits === '0' ? -1 : 1) * direction;
  }
  const leftMagnitude = BigInt(left.digits.length) + left.power;
  const rightMagnitude = BigInt(right.digits.length) + right.power;
  if (leftMagnitude !== rightMagnitude) return (leftMagnitude < rightMagnitude ? -1 : 1) * direction;
  for (let index = 0; index < Math.max(left.digits.length, right.digits.length); index++) {
    const a = left.digits[index] ?? '0';
    const b = right.digits[index] ?? '0';
    if (a !== b) return (a < b ? -1 : 1) * direction;
  }
  return 0;
}

const numericTypes = new Set(['int', 'int2', 'int4', 'int8', 'tinyint', 'smallint', 'mediumint', 'integer', 'bigint', 'unsigned big int', 'int64', 'numeric', 'decimal', 'float', 'float4', 'float8', 'real', 'double', 'double precision', 'oid']);

// Keep source indices so a selected cell follows its row after a displayed-result sort.
export function sortedRowIndices(rows: CellValue[][], column: number, dataType: string, descending: boolean): number[] {
  const isNumeric = numericTypes.has(dataType.toLowerCase().replace(/\s*\([^)]*\)$/, '').trim());
  const entries = rows.map((row, index) => ({ index, value: row[column] ?? null, numeric: isNumeric && row[column] !== null ? numericKey(row[column] ?? '') : null }));
  entries.sort((left, right) => {
    if (left.value === null || right.value === null) {
      return left.value === right.value ? left.index - right.index : left.value === null ? 1 : -1;
    }
    const comparison = left.numeric && right.numeric
      ? compareNumbers(left.numeric, right.numeric)
      : left.value === right.value ? 0 : left.value < right.value ? -1 : 1;
    return (descending ? -comparison : comparison) || left.index - right.index;
  });
  return entries.map(entry => entry.index);
}

export function hasAffectedRows(commandTag: string): boolean {
  return /^(INSERT|REPLACE|UPDATE|DELETE|MERGE)(?:\s|$)/i.test(commandTag.trim());
}

export function resultStatus(result: { columns: unknown[]; rowCount: number; rowsAffected: string; commandTag: string }): string {
  const counts: string[] = [];
  if (result.columns.length) counts.push(`${result.rowCount} rows returned`);
  if (hasAffectedRows(result.commandTag)) counts.push(`${result.rowsAffected} rows affected`);
  return counts.join(' · ') || `${result.commandTag || 'Statement'} completed`;
}
