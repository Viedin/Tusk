import assert from 'node:assert/strict';
import { Compartment, EditorState } from '@codemirror/state';
import { CompletionContext } from '@codemirror/autocomplete';
import { PostgreSQL, SQLite, schemaCompletionSource, sql } from '@codemirror/lang-sql';
import { queryCompletionSource, quoteIdentifier, sqlCompletionConfig, sqlDialect } from './sqlCompletion.ts';

const config = sqlCompletionConfig([
  { schema: 'public', name: 'events', type: 'table' },
  { schema: 'public', name: 'order', type: 'table' },
  { schema: 'Odd.schema', name: 'Order Lines', type: 'table' },
  { schema: 'Odd.schema', name: 'Order "Lines', type: 'table' },
  { schema: '__proto__', name: 'self', type: 'view' },
  { schema: 'constructor', name: 'toString', type: 'table' },
], [
  { schema: 'public', table: 'events', name: 'id', dataType: 'bigint' },
  { schema: 'public', table: 'events', name: 'select', dataType: 'text' },
  { schema: 'Odd.schema', table: 'Order Lines', name: 'A "column', dataType: 'numeric' },
]);

async function completions(doc, pos = doc.length) {
  const state = EditorState.create({ doc, extensions: [sql(config)] });
  return (await schemaCompletionSource(config)(new CompletionContext(state, pos, true)))?.options ?? [];
}

assert.equal(quoteIdentifier('a"b'), '"a""b"');
const top = await completions('SELECT * FROM ');
assert(top.some(option => option.displayLabel === 'events' && option.apply === '"events"'));
assert(top.some(option => option.displayLabel === 'order' && option.apply === '"order"'));
assert(top.some(option => option.displayLabel === 'Odd.schema' && option.apply === '"Odd.schema"'));
assert(top.some(option => option.displayLabel === '__proto__'));
assert(top.some(option => option.displayLabel === 'constructor'));
const alias = await completions('SELECT e. FROM public.events AS e', 'SELECT e.'.length);
assert(alias.some(option => option.displayLabel === 'id' && option.detail === 'bigint'));
assert(alias.some(option => option.displayLabel === 'select' && option.apply === '"select"'));
const quotedTable = await completions('SELECT * FROM "Odd.schema".');
assert(quotedTable.some(option => option.displayLabel === 'Order "Lines' && option.apply === '"Order ""Lines"'));
const quoted = await completions('SELECT "Odd.schema"."Order Lines".');
assert(quoted.some(option => option.displayLabel === 'A "column' && option.apply === '"A ""column"'));
const insideQuote = await completions('SELECT "Odd.schema"."Order Lines"."A');
assert(insideQuote.some(option => option.label === '"A ""column"'));
assert.deepEqual(await schemaCompletionSource(sqlCompletionConfig([], []))(new CompletionContext(EditorState.create({ extensions: [sql()] }), 0, true)), { from: 0, options: [], validFor: /^\w*$/ });
console.log('SQL completion checks passed');

{
  const tables = [{ schema: 'main', name: 'Assets', type: 'table' }];
  const columns = [{ schema: 'main', table: 'Assets', name: 'AssetId', dataType: 'INTEGER' }];
  const config = sqlCompletionConfig(tables, columns, 'sqlite');
  assert.equal(config.dialect, SQLite);
  assert.equal(config.defaultSchema, 'main');
  assert.equal(sqlDialect('sqlite'), SQLite);
  const doc = 'SELECT  FROM assets';
  const state = EditorState.create({ doc, extensions: [sql(config)] });
  const result = await queryCompletionSource(tables, columns, 'sqlite')(new CompletionContext(state, 7, true));
  assert(result?.options.some(option => option.displayLabel === 'AssetId' && option.apply === '"AssetId"'));
}

// Exercise the actual language-data source for automatic and explicit completion.
const queryTables = [
  { schema: 'public', name: 'Assets', type: 'table' },
  { schema: 'public', name: 'events', type: 'table' },
  { schema: 'Odd.schema', name: 'Order Lines', type: 'table' },
  { schema: 'public', name: 'assets', type: 'table' },
];
const queryColumns = [
  { schema: 'public', table: 'Assets', name: 'Id', dataType: 'bigint' },
  { schema: 'public', table: 'Assets', name: 'Name', dataType: 'text' },
  { schema: 'public', table: 'events', name: 'id', dataType: 'bigint' },
  { schema: 'public', table: 'events', name: 'EventOnly', dataType: 'text' },
  { schema: 'Odd.schema', table: 'Order Lines', name: 'Id', dataType: 'bigint' },
  { schema: 'Odd.schema', table: 'Order Lines', name: 'A "column', dataType: 'text' },
  { schema: 'public', table: 'assets', name: 'lowercase_only', dataType: 'text' },
];

async function queryCompletions(doc, { pos = doc.length, explicit = true, tables = queryTables, columns = queryColumns } = {}) {
  const source = queryCompletionSource(tables, columns);
  const state = EditorState.create({ doc, extensions: [sql({ dialect: PostgreSQL }), PostgreSQL.language.data.of({ autocomplete: source })] });
  const results = await Promise.all(state.languageDataAt('autocomplete', pos).map(completion => completion(new CompletionContext(state, pos, explicit))));
  return results.filter(Boolean).flatMap(result => result.options);
}

for (const explicit of [false, true]) {
  const exactDoc = 'SELECT * FROM "Assets" ORDER BY "Assets"."Id" ASC';
  const exact = await queryCompletions(exactDoc, { pos: exactDoc.indexOf('"Assets"."Id"') + '"Assets"."Id'.length, explicit });
  assert(exact.some(option => option.label === '"Id"'), 'existing qualified completion remains available');
  const unqualified = await queryCompletions('SELECT * FROM "Assets" ORDER BY Id', { explicit });
  assert(unqualified.some(option => option.displayLabel === 'Id' && option.apply === '"Id"'), 'Id inserts the unqualified quoted column');
  assert(!unqualified.some(option => option.displayLabel === 'EventOnly'));
  const selectDoc = 'SELECT I FROM "Assets"';
  const beforeFrom = await queryCompletions(selectDoc, { pos: 'SELECT I'.length, explicit });
  assert(beforeFrom.some(option => option.apply === '"Id"'), 'columns complete before a later FROM clause');
  const lowercase = await queryCompletions('SELECT * FROM "Assets" ORDER BY id', { explicit });
  assert(lowercase.some(option => option.displayLabel === 'Id' && option.apply === '"Id"'), 'lowercase input can discover mixed-case columns');
  const aliasColumns = await queryCompletions('SELECT * FROM public."Assets" AS a ORDER BY I', { explicit });
  assert(aliasColumns.some(option => option.apply === '"Id"'));
  const implicitAlias = await queryCompletions('SELECT * FROM "Assets" a ORDER BY I', { explicit });
  assert(implicitAlias.some(option => option.apply === '"Id"'));
  const qualifiedSchema = await queryCompletions('SELECT * FROM "Odd.schema"."Order Lines" ORDER BY I', { explicit });
  assert(qualifiedSchema.some(option => option.apply === '"Id"'));
  const join = await queryCompletions('SELECT * FROM "Assets" a JOIN events e ON a."Id" = e.id ORDER BY I', { explicit });
  assert(join.some(option => option.apply === '"a"."Id"'));
  assert(join.some(option => option.apply === '"e"."id"'));
  assert(!join.some(option => option.apply === '"Id"' || option.apply === '"id"'), 'joins do not suggest potentially ambiguous bare columns');
}

const actualLowercase = await queryCompletions('SELECT * FROM "assets" ORDER BY I');
assert(!actualLowercase.some(option => option.apply === '"Id"'), 'quoted table resolution stays case sensitive');
assert(actualLowercase.some(option => option.apply === '"lowercase_only"'));
const unquotedLowercase = await queryCompletions('SELECT * FROM Assets ORDER BY I');
assert(unquotedLowercase.some(option => option.apply === '"lowercase_only"'), 'unquoted relation names fold to lowercase');
const currentStatement = await queryCompletions('SELECT * FROM "Assets"; SELECT * FROM events ORDER BY I');
assert(currentStatement.some(option => option.apply === '"EventOnly"'));
assert(!currentStatement.some(option => option.apply === '"Id"'));
const comments = await queryCompletions('SELECT \'FROM "Assets";\', $$FROM "Assets";$$ FROM events /* JOIN "Assets" */ -- FROM "Assets"\n ORDER BY I');
assert(comments.some(option => option.apply === '"EventOnly"'));
assert(!comments.some(option => option.apply === '"Id"'));
for (const doc of [
  'SELECT \'FROM "Assets"\' ORDER BY I',
  'WITH x AS (SELECT * FROM "Assets") SELECT * FROM x ORDER BY I',
  'SELECT * FROM (SELECT * FROM "Assets") x ORDER BY I',
  'SELECT * FROM "Assets" UNION SELECT * FROM events ORDER BY I',
  'SELECT * FROM "Assets";',
]) {
  const options = await queryCompletions(doc);
  assert(!options.some(option => option.apply === '"Id"'), `no guessed global column context for ${doc}`);
}
assert.deepEqual(await queryCompletions('SELECT * FROM "Assets" WHERE \'Id', { explicit: true }), [], 'no completion inside a string');
assert.deepEqual(await queryCompletions('SELECT * FROM "Assets" -- Id', { explicit: true }), [], 'no completion inside a comment');
const cleared = await queryCompletions('SELECT * FROM "Assets" ORDER BY I', { tables: [], columns: [] });
assert(!cleared.some(option => option.apply === '"Id"'), 'cleared catalog removes contextual columns');
const compartment = new Compartment();
let liveState = EditorState.create({
  doc: 'SELECT * FROM "Assets" ORDER BY I',
  extensions: [sql({ dialect: PostgreSQL }), compartment.of(PostgreSQL.language.data.of({ autocomplete: queryCompletionSource(queryTables, queryColumns) }))],
});
async function liveOptions(state) {
  return (await Promise.all(state.languageDataAt('autocomplete', state.doc.length).map(source => source(new CompletionContext(state, state.doc.length, true))))).filter(Boolean).flatMap(result => result.options);
}
assert((await liveOptions(liveState)).some(option => option.apply === '"Id"'));
liveState = liveState.update({ effects: compartment.reconfigure(PostgreSQL.language.data.of({ autocomplete: queryCompletionSource([], []) })) }).state;
assert(!(await liveOptions(liveState)).some(option => option.apply === '"Id"'), 'reconfiguring the existing editor removes disconnected catalog columns');
const explicitEmpty = await queryCompletions('SELECT * FROM "Assets" ORDER BY ', { explicit: true });
assert(explicitEmpty.some(option => option.apply === '"Id"'), 'Ctrl+Space at an empty expression offers current table columns');
console.log('Query-aware column completion checks passed');
