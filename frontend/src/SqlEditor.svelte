<script lang="ts">
  import { onMount } from 'svelte';
  import { basicSetup, EditorView } from 'codemirror';
  import { Compartment } from '@codemirror/state';
  import { keymap, placeholder } from '@codemirror/view';
  import { PostgreSQL, sql } from '@codemirror/lang-sql';
  import { startCompletion } from '@codemirror/autocomplete';
  import { HighlightStyle, syntaxHighlighting } from '@codemirror/language';
  import { tags } from '@lezer/highlight';
  import { queryCompletionSource } from './sqlCompletion';

  let { value = $bindable(''), disabled = false, tables = [], columns = [], onrun }: {
    value?: string;
    disabled?: boolean;
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
    return [
      sql({ dialect: PostgreSQL, upperCaseKeywords: true }),
      PostgreSQL.language.data.of({ autocomplete: queryCompletionSource(tables, columns) }),
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
          { tag: tags.keyword, color: '#262626', fontWeight: '600' },
          { tag: [tags.string, tags.special(tags.string)], color: '#444444' },
          { tag: [tags.number, tags.bool, tags.null], color: '#4a4a4a' },
          { tag: [tags.typeName, tags.standard(tags.name)], color: '#3e3e3e' },
          { tag: tags.comment, color: '#555555', fontStyle: 'italic' },
          { tag: tags.operator, color: '#555555' },
        ])),
        EditorView.contentAttributes.of({ 'aria-label': 'SQL statement', spellcheck: 'false' }),
        EditorView.updateListener.of(update => {
          if (update.docChanged) value = update.state.doc.toString();
        }),
        EditorView.theme({
          '&': { height: '100%', fontSize: '14px', color: '#1f1f1f', backgroundColor: '#ffffff' },
          '.cm-scroller': { overflow: 'auto', fontFamily: 'var(--mono)', lineHeight: '1.75' },
          '.cm-content': { padding: '14px 0' },
          '.cm-line': { padding: '0 16px' },
          '.cm-gutters': { backgroundColor: '#fafafa', color: '#666666', borderRight: '1px solid #e9e9e9' },
          '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: '#00000005' },
          '.cm-cursor, .cm-dropCursor': { borderLeftColor: '#202020' },
          '&.cm-focused': { outline: 'none' },
          '& .cm-selectionLayer .cm-selectionBackground': { backgroundColor: '#e7e7e7' },
          '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground': { backgroundColor: '#cfcfcf' },
          '.cm-content ::selection': { backgroundColor: '#cfcfcf' },
          '.cm-selectionMatch': { backgroundColor: '#e5e5e5' },
          '.cm-tooltip': { border: '1px solid #cccccc', borderRadius: '5px', backgroundColor: '#ffffff', boxShadow: '0 4px 12px #00000012', color: '#1f1f1f' },
          '.cm-tooltip-autocomplete > ul': { fontFamily: 'var(--mono)', fontSize: '13px', maxHeight: '240px', maxWidth: 'min(650px, 85vw)' },
          '.cm-tooltip-autocomplete > ul > li': { padding: '6px 10px' },
          '.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: '#e8e8e8', color: '#1f1f1f' },
          '.cm-completionDetail': { marginLeft: '16px', color: '#666666', fontFamily: 'var(--ui)', fontSize: '12px' },
          '.cm-completionMatchedText': { color: '#111111', textDecoration: 'none', fontWeight: '600' },
          '.cm-searchMatch': { backgroundColor: '#e7e7e7' },
          '.cm-searchMatch-selected': { backgroundColor: '#cccccc' },
          '.cm-panels': { color: '#1f1f1f', backgroundColor: '#f3f3f3' },
          '.cm-textfield': { color: '#1f1f1f', backgroundColor: '#ffffff', border: '1px solid #d5d5d5' },
        }, { dark: false }),
      ],
    });
    return () => view?.destroy();
  });
</script>

<div class="editor-host" bind:this={host}></div>

<style>
  .editor-host { height: 100%; min-height: 150px; overflow: hidden; }
</style>
