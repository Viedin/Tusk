export function rowsToCSV(rows: readonly (readonly (string | null)[])[], headers?: readonly string[]): string {
  return (headers ? [headers, ...rows] : rows).map(row => row.map(value => {
    if (value === null) return '';
    return value === '' || /[,"\r\n]/.test(value) ? `"${value.replaceAll('"', '""')}"` : value;
  }).join(',')).join('\r\n');
}
