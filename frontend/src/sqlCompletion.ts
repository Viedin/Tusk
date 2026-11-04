import { PostgreSQL, schemaCompletionSource, type SQLConfig, type SQLNamespace } from '@codemirror/lang-sql';
import { CompletionContext, type Completion, type CompletionSource } from '@codemirror/autocomplete';
import { syntaxTree } from '@codemirror/language';
import type { SyntaxNode } from '@lezer/common';

type Table = { schema: string; name: string; type: string };
type Column = { schema: string; table: string; name: string; dataType?: string };
type Namespace = Record<string, SQLNamespace>;

export function quoteIdentifier(name: string): string {
  return `"${name.replaceAll('"', '""')}"`;
}

function identifier(name: string, type: string, detail: string): Completion {
  return { label: name.replaceAll('"', '""'), displayLabel: name, apply: quoteIdentifier(name), type, detail };
}

export function sqlCompletionConfig(tables: readonly Table[], columns: readonly Column[]): SQLConfig {
  // shortcut: CodeMirror cannot resolve context after embedded-quote identifiers; revisit when its SQL parser supports them.
  const schema: Namespace = Object.create(null);
  const children = new Map<string, Namespace>();
  const fields = new Map<string, Map<string, Completion[]>>();

  for (const table of tables) {
    let namespace = children.get(table.schema);
    if (!namespace) {
      namespace = Object.create(null) as Namespace;
      children.set(table.schema, namespace);
      schema[table.schema.replaceAll('"', '""').replaceAll('.', '\\.')] = {
        self: identifier(table.schema, 'namespace', 'schema'), children: namespace,
      };
    }
    let schemaFields = fields.get(table.schema);
    if (!schemaFields) fields.set(table.schema, schemaFields = new Map());
    const tableFields: Completion[] = [];
    schemaFields.set(table.name, tableFields);
    // CodeMirror treats dots as paths and retains doubled quotes when resolving identifiers.
    namespace[table.name.replaceAll('"', '""').replaceAll('.', '\\.')] = {
      self: identifier(table.name, 'class', `${table.schema} · ${table.type}`), children: tableFields,
    };
  }
  for (const column of columns) {
    fields.get(column.schema)?.get(column.table)?.push(identifier(column.name, 'property', column.dataType ?? 'column'));
  }
  return { dialect: PostgreSQL, schema, defaultSchema: children.has('public') ? 'public' : undefined, upperCaseKeywords: true };
}

/** Infer only simple relations in this parser-delimited SELECT statement. */
function statementRelations(context: CompletionContext, tables: readonly Table[]): Relation[] {
  let node: SyntaxNode | null = syntaxTree(context.state).resolveInner(context.pos, -1);
  for (let ancestor: SyntaxNode | null = node; ancestor; ancestor = ancestor.parent) {
    if (ancestor.name === 'String' || ancestor.name.includes('Comment')) return [];
    if (ancestor.name === 'Parens' && childNodes(ancestor).some(child => keyword(context, child) === 'select')) return [];
    if (ancestor.name === 'Statement') { node = ancestor; break; }
  }
  if (node?.name === 'Script') {
    for (const statement of childNodes(node)) {
      if (statement.from > context.pos) break;
      if (statement.name === 'Statement') node = statement;
    }
  }
  if (node?.name !== 'Statement') return [];
  const tokens = childNodes(node).filter(token => !token.name.includes('Comment'));
  if (keyword(context, tokens[0]) !== 'select' || tokens.some(token => ['union', 'intersect', 'except'].includes(keyword(context, token)))) return [];
  if (tokens.some(token => token.name === ';' && context.pos >= token.to)) return [];
  const relations: Relation[] = [];
  let inFrom = false;
  let expectsTable = false;
  for (let index = 0; index < tokens.length; index++) {
    const token = tokens[index];
    const word = keyword(context, token);
    if (word === 'from') { inFrom = true; expectsTable = true; continue; }
    if (!inFrom) continue;
    if (['where', 'group', 'having', 'order', 'limit', 'offset', 'fetch', 'for', 'window'].includes(word)) break;
    if (word === 'join' || context.state.sliceDoc(token.from, token.to) === ',') { expectsTable = true; continue; }
    if (!expectsTable || word === 'only') continue;
    const path = identifierPath(context, token);
    if (!path || path.length > 2 || tokens[index + 1]?.name === 'Parens') return [];
    const candidates = tables.filter(table => table.name === path.at(-1) && (path.length === 1 || table.schema === path[0]));
    const table = path.length === 1 ? candidates.find(table => table.schema === 'public') ?? (candidates.length === 1 ? candidates[0] : undefined) : candidates[0];
    if (!table) return [];
    let alias: string | undefined;
    if (keyword(context, tokens[index + 1]) === 'as') index++;
    const aliasPath = identifierPath(context, tokens[index + 1]);
    if (aliasPath?.length === 1) { alias = aliasPath[0]; index++; }
    relations.push({ table, alias, qualified: path.length === 2 });
    expectsTable = false;
  }
  return expectsTable ? [] : relations;
}

type Relation = { table: Table; alias?: string; qualified: boolean };

function childNodes(node: SyntaxNode): SyntaxNode[] {
  const children: SyntaxNode[] = [];
  for (let child = node.firstChild; child; child = child.nextSibling) children.push(child);
  return children;
}

function keyword(context: CompletionContext, node?: SyntaxNode): string {
  return node?.name === 'Keyword' ? context.state.sliceDoc(node.from, node.to).toLowerCase() : '';
}

function identifierPath(context: CompletionContext, node?: SyntaxNode): string[] | null {
  if (!node) return null;
  if (node.name === 'CompositeIdentifier') {
    const parts = childNodes(node).filter(child => child.name !== '.').map(child => identifierPath(context, child));
    return parts.every(part => part?.length === 1) ? parts.flatMap(part => part!) : null;
  }
  const text = context.state.sliceDoc(node.from, node.to);
  return node.name === 'Identifier' ? [text.toLowerCase()]
    : node.name === 'QuotedIdentifier' && text.endsWith('"') ? [text.slice(1, -1).replaceAll('""', '"')] : null;
}

/** Add statement-local columns while retaining CodeMirror's qualified-name resolution. */
export function queryCompletionSource(tables: readonly Table[], columns: readonly Column[]): CompletionSource {
  const config = sqlCompletionConfig(tables, columns);
  const fallback = schemaCompletionSource(config);
  let activeTable: Table | undefined;
  let tableSource: CompletionSource;
  return async context => {
    const at = syntaxTree(context.state).resolveInner(context.pos, -1);
    if (at.name === 'String' || at.name.includes('Comment')) return null;
    const relations = statementRelations(context, tables);
    if (relations.length === 1) {
      const table = relations[0].table;
      if (table !== activeTable) {
        activeTable = table;
        tableSource = schemaCompletionSource({
          ...config,
          defaultSchema: table.schema.replaceAll('"', '""'),
          defaultTable: table.name.replaceAll('"', '""'),
        });
      }
      const result = await tableSource!(context);
      return result ? { ...result, options: result.options.map(option => option.type === 'property' ? { ...option, boost: 10 } : option) } : null;
    }
    const result = await fallback(context);
    if (!result || relations.length < 2 || at.name === '.' || at.parent?.name === 'CompositeIdentifier') return result;
    const qualified = relations.flatMap(relation => columns
      .filter(column => column.schema === relation.table.schema && column.table === relation.table.name)
      .map(column => {
        const path = relation.alias ? [relation.alias] : relation.qualified ? [relation.table.schema, relation.table.name] : [relation.table.name];
        return {
          label: `${path.join('.')}.${column.name}`,
          apply: [...path, column.name].map(quoteIdentifier).join('.'),
          type: 'property',
          detail: column.dataType ?? 'column',
        };
      }));
    return { ...result, options: [...result.options, ...qualified] };
  };
}
