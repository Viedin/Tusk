<script lang="ts">
  import { onMount } from 'svelte';
  import { basicSetup, EditorView } from 'codemirror';
  import { Compartment } from '@codemirror/state';
  import { keymap, placeholder } from '@codemirror/view';
  import { sql } from '@codemirror/lang-sql';
  import { startCompletion } from '@codemirror/autocomplete';
  import { HighlightStyle, syntaxHighlighting } from '@codemirror/language';
  import { tags } from '@lezer/highlight';
  import { queryCompletionSource, sqlDialect } from './sqlCompletion';

  let { value = $bindable(''), disabled = false, provider = 'postgres', tables = [], columns = [], onrun }: {
    value?: string;
    disabled?: boolean;
    provider?: string;
    tables?: { schema: string; name: string; type: string }[];
    columns?: { schema: string; table: string; name: string; dataType?: string }[];
    onrun: (statement: string) => void;
  } = $props();

  let host: HTMLDivElement;
  let view: EditorView | undefined;
  const language = new Compartment();

  export function getQuery(): string {
    if (!view) return value.trim();
    const selection = view.state.sliceDoc(view.state.selection.main.from, view.state.selection.main.to);
    return (selection.trim() ? selection : view.state.doc.toString()).trim();
  }

  export function focus(): void { view?.focus(); }

  function sqlSupport() {
    const dialect = sqlDialect(provider);
    return [
      sql({ dialect, upperCaseKeywords: true }),
      dialect.language.data.of({ autocomplete: queryCompletionSource(tables, columns, provider) }),
    ];
  }

  $effect(() => {
    const support = sqlSupport();
    view?.dispatch({ effects: language.reconfigure(support) });
  });

  $effect(() => {
    const text = value;
    if (view && view.state.doc.toString() !== text) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } });
    }
  });

  onMount(() => {
    view = new EditorView({
      parent: host,
      doc: value,
      extensions: [
        keymap.of([
          { key: 'Mod-Enter', run: () => { if (!disabled) onrun(getQuery()); return true; } },
          { key: 'Ctrl-Space', run: startCompletion },
        ]),
        basicSetup,
        placeholder('Write a SQL statement…'),
        language.of(sqlSupport()),
        syntaxHighlighting(HighlightStyle.define([
          { tag: tags.keyword, color: 'var(--sql-keyword)', fontWeight: '600' },
          { tag: [tags.string, tags.special(tags.string)], color: 'var(--sql-string)' },
          { tag: [tags.number, tags.bool, tags.null], color: 'var(--sql-literal)' },
          { tag: [tags.typeName, tags.standard(tags.name)], color: 'var(--sql-type)' },
          { tag: tags.comment, color: 'var(--sql-comment)', fontStyle: 'italic' },
          { tag: tags.operator, color: 'var(--sql-comment)' },
        ])),
        EditorView.contentAttributes.of({ 'aria-label': 'SQL statement', spellcheck: 'false' }),
        EditorView.updateListener.of(update => {
          if (update.docChanged) value = update.state.doc.toString();
        }),
        EditorView.theme({
          '&': { height: '100%', fontSize: '14px', color: 'var(--ink)', backgroundColor: 'var(--background)' },
          '.cm-scroller': { overflow: 'auto', fontFamily: 'var(--mono)', lineHeight: '1.75' },
          '.cm-content': { padding: '14px 0', caretColor: 'var(--ink)' },
          '.cm-line': { padding: '0 16px' },
          '.cm-gutters': { backgroundColor: 'var(--surface)', color: 'var(--muted)', borderRight: '1px solid var(--border)' },
          '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: 'var(--sql-active-line)' },
          '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--ink)' },
          '&.cm-focused': { outline: 'none' },
          '& .cm-selectionLayer .cm-selectionBackground': { backgroundColor: 'var(--selection)' },
          '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground': { backgroundColor: 'var(--selection-focused)' },
          '.cm-content ::selection': { backgroundColor: 'var(--selection-focused)' },
          '.cm-selectionMatch': { backgroundColor: 'var(--accent-soft)' },
          '.cm-tooltip': { border: '1px solid var(--control-border)', borderRadius: '5px', backgroundColor: 'var(--background)', boxShadow: '0 4px 12px var(--shadow)', color: 'var(--ink)' },
          '.cm-tooltip-autocomplete > ul': { fontFamily: 'var(--mono)', fontSize: '13px', maxHeight: '240px', maxWidth: 'min(650px, 85vw)' },
          '.cm-tooltip-autocomplete > ul > li': { padding: '6px 10px' },
          '.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: 'var(--selection)', color: 'var(--ink)' },
          '.cm-completionDetail': { marginLeft: '16px', color: 'var(--muted)', fontFamily: 'var(--ui)', fontSize: '12px' },
          '.cm-completionMatchedText': { color: 'var(--ink)', textDecoration: 'none', fontWeight: '600' },
          '.cm-searchMatch': { backgroundColor: 'var(--selection)' },
          '.cm-searchMatch-selected': { backgroundColor: 'var(--selection-focused)' },
          '.cm-placeholder': { color: 'var(--subtle)' },
          '.cm-foldPlaceholder': { color: 'var(--muted)', backgroundColor: 'var(--hover)', borderColor: 'var(--border)' },
          '.cm-panels-top, .cm-panels-bottom': { borderColor: 'var(--border)' },
          '.cm-button': { color: 'var(--ink)', background: 'var(--background)', borderColor: 'var(--control-border)' },
          '.cm-button:hover': { background: 'var(--hover)' },
          '.cm-button:active': { background: 'var(--selection)' },
          '.cm-panels': { color: 'var(--ink)', backgroundColor: 'var(--hover)' },
          '.cm-textfield': { color: 'var(--ink)', backgroundColor: 'var(--background)', border: '1px solid var(--control-border)' },
        }),
      ],
    });
    return () => view?.destroy();
  });
</script>

<div class="editor-host" bind:this={host}></div>

<style>
  .editor-host {
    --sql-keyword: #262626;
    --sql-string: #444444;
    --sql-literal: #4a4a4a;
    --sql-type: #3e3e3e;
    --sql-comment: #555555;
    --sql-active-line: #00000005;
    height: 100%; min-height: 150px; overflow: hidden;
  }
  @media (prefers-color-scheme: dark) {
    .editor-host {
      --sql-keyword: #e5e5e5;
      --sql-string: #c8c8c8;
      --sql-literal: #bfbfbf;
      --sql-type: #cecece;
      --sql-comment: #a8a8a8;
      --sql-active-line: #ffffff06;
    }
  }
</style>
