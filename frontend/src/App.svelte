<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { Clipboard } from '@wailsio/runtime';
  import { Service } from '../bindings/tusk/internal/database';
  import { ConnectionsService } from '../bindings/tusk/internal/store';
  import { Service as ExportService } from '../bindings/tusk/internal/csvexport';
  import SqlEditor from './SqlEditor.svelte';
  import PanelDivider from './PanelDivider.svelte';
  import DatabaseIcon from './DatabaseIcon.svelte';
  import { hasAffectedRows, resultStatus, sortedRowIndices } from './results';
  import { rowsToCSV } from './csv';
  import { applyRowChanges, buildRowChanges, selectRowRange, updateCellEdit, type RowEdits } from './gridChanges';
  import { loadQueryTabs, saveQueryTabs } from './queryTabs';

  type SavedConnection = NonNullable<Awaited<ReturnType<typeof ConnectionsService.ListConnections>>>[number];
  type ConnectionInfo = Awaited<ReturnType<typeof Service.Connect>>;
  type TableInfo = NonNullable<Awaited<ReturnType<typeof Service.ListTables>>>[number];
  type CatalogColumn = NonNullable<Awaited<ReturnType<typeof Service.ListColumns>>>[number];
  type SortState = { columnIndex: number; columnName: string; descending: boolean };
  type RawResult = Awaited<ReturnType<typeof Service.RunQuery>>;
  type TableEditInfo = Awaited<ReturnType<typeof Service.GetTableEditInfo>>;
  type QueryResult = Omit<RawResult, 'columns' | 'rows'> & { columns: NonNullable<RawResult['columns']>; rows: (string | null)[][] };
  type RowMenu = { cells: (string | null)[]; rowNumber: number; sourceIndex: number; rowIndices: number[]; x: number; y: number; trigger: HTMLButtonElement };
  type QueryPanel = {
    id: number;
    text: string;
    result: QueryResult | null;
    sort: SortState | null;
    editInfo: TableEditInfo | null;
    cellEdits: RowEdits;
    deletedRows: number[];
    error: string;
    status: string;
    editor?: { getQuery(): string; focus(): void };
  };

  let nextQueryId = 1;
  function newQueryPanel(text = ''): QueryPanel {
    return { id: nextQueryId++, text, result: null, sort: null, editInfo: null, cellEdits: {}, deletedRows: [], error: '', status: 'Ready for your query' };
  }

  let queryPanels = $state<QueryPanel[]>([newQueryPanel()]);
  let activeQueryId = $state(1);
  const activeQuery = $derived(queryPanels.find(panel => panel.id === activeQueryId)!);
  const activeQueryNumber = $derived(queryPanels.indexOf(activeQuery) + 1);
  let runningQueryId = $state<number | null>(null);
  let pendingCloseQuery = $state<QueryPanel | null>(null);
  let queryCloseDialog = $state<HTMLDialogElement>();
  let queryStorageLoaded = $state(false);
  let queryStorageError = $state('');

  function normalizeResult(result: RawResult): QueryResult {
    return { ...result, columns: result.columns ?? [], rows: (result.rows ?? []).map(row => row ?? []) };
  }

  let connection = $state<ConnectionInfo | null>(null);
  let windowWidth = $state(1240);
  let windowHeight = $state(800);
  // shortcut: panel sizes last for this window; persist them when layout preferences are added.
  let sidebarWidth = $state(278);
  let connectionsHeight = $state(340);
  let editorHeight = $state(224);
  let detailWidth = $state(320);
  const maxSidebarWidth = $derived(Math.max(220, Math.min(480, windowWidth - 560)));
  const visibleSidebarWidth = $derived(Math.min(sidebarWidth, maxSidebarWidth));
  const maxConnectionsHeight = $derived(Math.max(240, windowHeight - (connection ? 240 : 170)));
  const visibleConnectionsHeight = $derived(Math.min(connectionsHeight, maxConnectionsHeight));
  const maxEditorHeight = $derived(Math.max(150, windowHeight - 340));
  const visibleEditorHeight = $derived(Math.min(editorHeight, maxEditorHeight));
  const maxDetailWidth = $derived(Math.max(220, Math.min(560, windowWidth - visibleSidebarWidth - 170)));
  let connectionURL = $state('');
  let connectionProvider = $state<'postgres' | 'sqlite'>('postgres');
  let connectionFilePath = $state('');
  let connectionName = $state('');
  let connectionColor = $state('');
  let editingSavedId = $state<string | null>(null);
  let replacePassword = $state(false);
  let editDialog = $state<HTMLDialogElement>();
  let savedSearch = $state('');
  let savedSwitcherOpen = $state(false);
  const connectionColors = [
    { name: 'None', value: '' }, { name: 'Blue', value: '#356cca' },
    { name: 'Green', value: '#287c58' }, { name: 'Amber', value: '#a56b18' },
    { name: 'Red', value: '#b94b51' }, { name: 'Purple', value: '#7952b3' },
  ];
  let savedConnections = $state<SavedConnection[]>([]);
  let savedLoading = $state(true);
  let storageError = $state('');
  let activeSavedId = $state<string | null>(null);
  let activeSettingsChanged = $state(false);
  let pendingDeleteId = $state<string | null>(null);
  const visibleSavedConnections = $derived(savedConnections.filter(saved => `${saved.name} ${saved.database} ${saved.host} ${saved.user} ${saved.filePath ?? ''}`.toLowerCase().includes(savedSearch.trim().toLowerCase())));
  const activeConnectionColor = $derived(savedConnections.find(saved => saved.id === activeSavedId)?.color || '#666666');
  let connectionMode = $state<'fields' | 'url'>('fields');
  let connectionHost = $state('localhost');
  let connectionPort = $state(5432);
  let connectionDatabase = $state('postgres');
  let connectionUser = $state('');
  let connectionPassword = $state('');
  let connectionSSLMode = $state('prefer');
  const canConnect = $derived(connectionProvider === 'sqlite' ? !!connectionFilePath.trim() : connectionMode === 'url'
    ? !!editingSavedId || !!connectionURL.trim()
    : !!connectionHost.trim() && !!connectionDatabase.trim() && !!connectionUser.trim()
      && Number.isInteger(connectionPort) && connectionPort >= 1 && connectionPort <= 65535);
  let tables = $state<TableInfo[]>([]);
  let availableSchemas = $state<string[]>([]);
  let catalogColumns = $state<CatalogColumn[]>([]);
  let expandedSchemas = $state<Record<string, boolean>>({});
  let search = $state('');
  let mode = $state<'table' | 'sql'>('sql');
  let selectedTable = $state<TableInfo | null>(null);
  let tableResult = $state<QueryResult | null>(null);
  let tableSort = $state<SortState | null>(null);
  let tableEditInfo = $state<TableEditInfo | null>(null);
  let cellEdits = $state<RowEdits>({});
  let deletedRows = $state<number[]>([]);
  const tablePendingChanges = $derived(tableResult ? buildRowChanges(tableResult.rows, cellEdits, deletedRows) : []);
  const queryPendingChanges = $derived(activeQuery.result ? buildRowChanges(activeQuery.result.rows, activeQuery.cellEdits, activeQuery.deletedRows) : []);
  const pendingChanges = $derived(mode === 'sql' ? queryPendingChanges : tablePendingChanges);
  const activeDeletedRows = $derived(mode === 'sql' ? activeQuery.deletedRows : deletedRows);
  const activeCellEdits = $derived(mode === 'sql' ? activeQuery.cellEdits : cellEdits);
  const sourceColumns = $derived(mode === 'sql' ? activeQuery.result?.deletionTarget?.columns ?? [] : tableResult?.columns.map(column => column.name) ?? []);
  const activeEditInfo = $derived(mode === 'sql' ? activeQuery.editInfo : tableEditInfo);
  const canEditRows = $derived(!!sourceColumns.length && !!activeEditInfo?.keyColumns?.length && !activeEditInfo.reason);
  const canRemoveRows = $derived(mode === 'table' ? canEditRows : !!activeQuery.result?.deletionTarget);
  const removalReason = $derived(mode === 'sql' ? activeQuery.result?.removalUnavailableReason || 'Include the table’s primary key in a SELECT from one table to remove rows.' : tableEditInfo?.reason || 'This table cannot be edited.');
  let selectedRows = $state<number[]>([]);
  let selectionAnchor = $state<number | null>(null);
  let dragSelection: { anchor: number; target: number; base: number[]; remove: boolean; startY: number; y: number; moved: boolean; frame: number } | null = null;
  let gridScroll = $state<HTMLDivElement>();
  let exportOpen = $state(false);
  let exportButton = $state<HTMLButtonElement>();
  let exportCSVButton = $state<HTMLButtonElement>();
  let cellEditor = $state<HTMLTextAreaElement>();
  let cellNullToggle = $state<HTMLInputElement>();
  const currentSort = $derived(mode === 'sql' ? activeQuery.sort : tableSort);
  const result = $derived(mode === 'sql' ? activeQuery.result : tableResult);
  const displayedRows = $derived.by(() => {
    if (!result) return [];
    const rows = applyRowChanges(result.rows, sourceColumns, activeCellEdits, []);
    const indices = mode === 'sql' && activeQuery.sort
      ? sortedRowIndices(rows, activeQuery.sort.columnIndex, result.columns[activeQuery.sort.columnIndex].dataType, activeQuery.sort.descending)
      : rows.map((_, index) => index);
    return indices.map(sourceIndex => ({ sourceIndex, cells: rows[sourceIndex] }));
  });
  let selectedCell = $state<{ column: string; type: string; value: string | null; row: number; columnIndex: number } | null>(null);
  let rowMenu = $state<RowMenu | null>(null);
  let rowMenuElement = $state<HTMLDivElement>();
  let rowCopyButton = $state<HTMLButtonElement>();
  const selectedRowNumber = $derived(displayedRows.findIndex(row => row.sourceIndex === selectedCell?.row) + 1);
  const editableCell = $derived(canEditRows && !!selectedCell && !activeDeletedRows.includes(selectedCell.row)
    && !activeEditInfo?.readOnlyColumns?.includes(sourceColumns[selectedCell.columnIndex]));
  const editReason = $derived(activeEditInfo?.reason || (selectedCell && activeEditInfo?.readOnlyColumns?.includes(sourceColumns[selectedCell.columnIndex])
    ? 'This column is generated by the database and cannot be edited.'
    : mode === 'sql' ? activeQuery.result?.removalUnavailableReason || 'Run a SELECT from one table that includes every primary-key column to edit cell values.' : ''));
  let busy = $state('');
  let cancelling = $state(false);
  const canCancel = $derived(!!busy && busy !== 'Saving connection…' && busy !== 'Saving changes…' && busy !== 'Deleting connection…' && busy !== 'Exporting rows…');
  let error = $state('');
  const workspaceError = $derived(error || (mode === 'sql' ? activeQuery.error : '') || queryStorageError);
  let status = $state('Ready to connect');

  $effect(() => {
    if (!queryStorageLoaded) return;
    const texts = queryPanels.map(panel => panel.text);
    const activeIndex = queryPanels.indexOf(activeQuery);
    try {
      saveQueryTabs(localStorage, texts, activeIndex);
      queryStorageError = '';
    } catch (cause) {
      queryStorageError = `Could not save SQL tabs. Keep a copy of your SQL before quitting. ${message(cause)}`;
    }
  });

  const searchTerm = $derived(search.trim().toLowerCase());
  const filteredTables = $derived(tables.filter(table => `${table.schema}.${table.name}`.toLowerCase().includes(searchTerm)));
  const schemas = $derived(availableSchemas.filter(schema => schema.toLowerCase().includes(searchTerm) || filteredTables.some(table => table.schema === schema)));

  function toggleSchema(event: MouseEvent, schema: string): void {
    event.preventDefault();
    if (!searchTerm) expandedSchemas = { ...expandedSchemas, [schema]: expandedSchemas[schema] === false };
  }

  async function loadCatalogColumns(): Promise<void> {
    catalogColumns = [];
    try { catalogColumns = await Service.ListColumns() ?? []; }
    catch (cause) { error = `Column autocomplete unavailable: ${message(cause)}`; }
  }

  function message(cause: unknown): string {
    return cause instanceof Error ? cause.message : String(cause);
  }

  async function perform(label: string, action: () => Promise<void>): Promise<void> {
    if (busy) return;
    closeRowMenu();
    busy = label;
    error = '';
    status = label;
    try { await action(); }
    catch (cause) { error = message(cause); status = 'Action failed'; }
    finally { busy = ''; cancelling = false; }
  }

  function clearResult(): void {
    if (mode === 'sql') activeQuery.result = null;
    else tableResult = null;
    selectedCell = null;
    resetSelection();
  }

  function resetSelection(): void {
    closeRowMenu();
    stopRowSelection();
    selectedRows = [];
    selectionAnchor = null;
    exportOpen = false;
  }

  function canLeaveRows(allQueries = false): boolean {
    const queryChanges = allQueries ? queryPanels.some(panel => panel.deletedRows.length || Object.keys(panel.cellEdits).length) : mode === 'sql' && queryPendingChanges.length;
    if (!tablePendingChanges.length && !queryChanges) return true;
    error = 'Save or undo your row changes before running another query, refreshing, or leaving these results.';
    return false;
  }

  function clearQueryResults(): void {
    for (const panel of queryPanels) {
      panel.result = null;
      panel.sort = null;
      panel.editInfo = null;
      panel.cellEdits = {};
      panel.deletedRows = [];
      panel.error = '';
      panel.status = 'Ready for your query';
    }
  }

  function scrollActiveQueryIntoView(): void {
    const tab = document.getElementById(`query-tab-${activeQueryId}`)?.parentElement;
    const list = tab?.parentElement;
    if (!tab || !list) return;
    if (tab.offsetLeft < list.scrollLeft) list.scrollLeft = tab.offsetLeft;
    else if (tab.offsetLeft + tab.offsetWidth > list.scrollLeft + list.clientWidth) {
      list.scrollLeft = tab.offsetLeft + tab.offsetWidth - list.clientWidth;
    }
  }

  async function selectQuery(panel: QueryPanel, focusEditor = true): Promise<void> {
    if (busy && runningQueryId === null) return;
    if (mode === 'table' && !canLeaveRows()) return;
    activeQueryId = panel.id;
    mode = 'sql';
    selectedCell = null;
    resetSelection();
    error = '';
    status = panel.status;
    await tick();
    if (focusEditor) panel.editor?.focus();
    else document.getElementById(`query-tab-${panel.id}`)?.focus();
    scrollActiveQueryIntoView();
  }

  async function addQuery(): Promise<void> {
    if ((busy && runningQueryId === null) || (mode === 'table' && !canLeaveRows())) return;
    queryPanels.push(newQueryPanel());
    await selectQuery(queryPanels[queryPanels.length - 1]);
  }

  function cancelQueryClose(): void {
    queryCloseDialog?.close();
    pendingCloseQuery = null;
  }

  async function closeQuery(panel: QueryPanel, confirmed = false): Promise<void> {
    if (busy || queryPanels.length === 1) return;
    if (panel.deletedRows.length || Object.keys(panel.cellEdits).length) { error = 'Save or undo this query’s row changes before closing it.'; return; }
    if (panel.text.trim() && !confirmed) {
      pendingCloseQuery = panel;
      await tick();
      queryCloseDialog?.showModal();
      return;
    }
    cancelQueryClose();
    const index = queryPanels.indexOf(panel);
    const next = queryPanels[index + 1] ?? queryPanels[index - 1];
    if (panel.id === activeQueryId) await selectQuery(next, false);
    else document.getElementById(`query-tab-${activeQueryId}`)?.focus();
    queryPanels = queryPanels.filter(item => item.id !== panel.id);
  }

  function queryTabKeydown(event: KeyboardEvent, index: number): void {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? queryPanels.length - 1
      : (index + (event.key === 'ArrowRight' ? 1 : -1) + queryPanels.length) % queryPanels.length;
    void selectQuery(queryPanels[next], false);
  }

  onMount(() => {
    try {
      const saved = loadQueryTabs(localStorage);
      if (saved) {
        queryPanels = saved.texts.map(text => newQueryPanel(text));
        activeQueryId = queryPanels[saved.activeIndex].id;
      }
      queryStorageLoaded = true;
    } catch (cause) {
      queryStorageError = `Could not restore SQL tabs. Existing saved data was kept; new edits will not be saved. Keep a copy of your SQL. ${message(cause)}`;
    }
    void loadSavedConnections();
  });

  async function loadSavedConnections(): Promise<void> {
    savedLoading = true;
    storageError = '';
    try { savedConnections = await ConnectionsService.ListConnections() ?? []; }
    catch (cause) { storageError = message(cause); }
    finally { savedLoading = false; }
  }

  function connectionOptions() {
    return {
      provider: connectionProvider,
      filePath: connectionProvider === 'sqlite' ? connectionFilePath.trim() : '',
      host: connectionHost.trim(),
      port: connectionPort,
      database: connectionDatabase,
      user: connectionUser,
      password: connectionPassword,
      sslMode: connectionSSLMode,
    };
  }

  function connectionEndpoint(info: { provider?: string; filePath?: string; host: string; port: number }): string {
    return info.provider === 'sqlite' ? info.filePath ?? '' : `${info.host}:${info.port}`;
  }

  async function loadConnection(info: ConnectionInfo, savedId: string | null = null): Promise<void> {
    if (editingSavedId) cancelEdit();
    connection = info;
    activeSavedId = savedId;
    activeSettingsChanged = false;
    savedSwitcherOpen = false;
    savedSearch = '';
    pendingDeleteId = null;
    connectionURL = '';
    connectionPassword = '';
    mode = 'sql';
    selectedTable = null;
    tables = [];
    availableSchemas = [];
    catalogColumns = [];
    expandedSchemas = {};
    search = '';
    clearQueryResults();
    tableResult = null;
    tableSort = null;
    cellEdits = {};
    deletedRows = [];
    tableEditInfo = null;
    clearResult();
    availableSchemas = await Service.ListSchemas() ?? [];
    tables = await Service.ListTables() ?? [];
    await loadCatalogColumns();
    status = `Connected to ${connection.database}`;
    await tick();
    scrollActiveQueryIntoView();
    activeQuery.editor?.focus();
  }

  async function connect(): Promise<void> {
    if (!canConnect) return;
    await perform('Connecting…', async () => {
      const info = connectionProvider === 'postgres' && connectionMode === 'url'
        ? await Service.Connect(connectionURL.trim())
        : await Service.ConnectWithOptions(connectionOptions());
      await loadConnection(info);
    });
  }

  async function connectSaved(saved: SavedConnection): Promise<void> {
    if (!canLeaveRows(true)) return;
    await perform('Connecting…', async () => {
      await loadConnection(await ConnectionsService.ConnectSaved(saved.id), saved.id);
    });
  }

  async function saveAndConnect(): Promise<void> {
    if (!canConnect || !connectionName.trim() || savedLoading) return;
    await perform('Saving connection…', async () => {
      const saved = await ConnectionsService.SaveConnection({
        name: connectionName.trim(),
        color: connectionColor,
        mode: connectionProvider === 'sqlite' ? 'file' : connectionMode,
        url: connectionProvider === 'postgres' && connectionMode === 'url' ? connectionURL.trim() : '',
        options: connectionOptions(),
      });
      connectionURL = '';
      connectionPassword = '';
      connectionName = '';
      storageError = '';
      savedConnections = [...savedConnections.filter(item => item.id !== saved.id), saved]
        .sort((left, right) => left.name.localeCompare(right.name));
      busy = 'Connecting…';
      status = 'Connecting…';
      await loadConnection(await ConnectionsService.ConnectSaved(saved.id), saved.id);
    });
  }

  async function startEdit(saved: SavedConnection): Promise<void> {
    if (busy) return;
    editingSavedId = saved.id;
    connectionName = saved.name;
    connectionColor = saved.color ?? '';
    connectionProvider = saved.provider === 'sqlite' ? 'sqlite' : 'postgres';
    connectionFilePath = saved.filePath ?? '';
    connectionMode = saved.mode === 'url' ? 'url' : 'fields';
    connectionHost = saved.host;
    connectionPort = saved.port;
    connectionDatabase = saved.database;
    connectionUser = saved.user;
    connectionSSLMode = saved.sslMode;
    connectionPassword = '';
    connectionURL = '';
    replacePassword = false;
    pendingDeleteId = null;
    error = '';
    await tick();
    if (connection) editDialog?.showModal();
    document.getElementById('connection-name')?.focus();
  }

  function cancelEdit(): void {
    editDialog?.close();
    editingSavedId = null;
    connectionName = '';
    connectionColor = '';
    connectionProvider = 'postgres';
    connectionFilePath = '';
    connectionMode = 'fields';
    connectionHost = 'localhost';
    connectionPort = 5432;
    connectionDatabase = 'postgres';
    connectionUser = '';
    connectionSSLMode = 'prefer';
    connectionPassword = '';
    connectionURL = '';
    replacePassword = false;
    error = '';
  }

  async function saveEditedConnection(): Promise<void> {
    if (!editingSavedId || !canConnect || !connectionName.trim()) return;
    await perform('Saving changes…', async () => {
      const previous = savedConnections.find(item => item.id === editingSavedId);
      const saved = await ConnectionsService.UpdateConnection(editingSavedId!, {
        name: connectionName.trim(), color: connectionColor,
        options: connectionOptions(), url: connectionURL.trim(), replacePassword,
      });
      savedConnections = [...savedConnections.filter(item => item.id !== saved.id), saved]
        .sort((left, right) => left.name.localeCompare(right.name));
      if (activeSavedId === saved.id && connection) {
        activeSettingsChanged ||= replacePassword || !!connectionURL.trim() || previous?.sslMode !== saved.sslMode
          || saved.host !== connection.host || saved.port !== connection.port
          || saved.database !== connection.database || saved.user !== connection.user
          || saved.filePath !== connection.filePath;
      }
      storageError = '';
      cancelEdit();
      status = `Saved “${saved.name}”. Connection settings apply next time you connect.`;
    });
  }

  async function deleteSaved(saved: SavedConnection): Promise<void> {
    if (savedLoading) return;
    await perform('Deleting connection…', async () => {
      await ConnectionsService.DeleteConnection(saved.id);
      savedConnections = savedConnections.filter(item => item.id !== saved.id);
      if (activeSavedId === saved.id) activeSavedId = null;
      pendingDeleteId = null;
      status = `Deleted saved connection “${saved.name}”`;
    });
  }

  async function disconnect(): Promise<void> {
    if (!canLeaveRows(true)) return;
    await perform('Disconnecting…', async () => {
      await Service.Disconnect();
      connection = null;
      activeSavedId = null;
      mode = 'sql';
      tables = [];
      availableSchemas = [];
      catalogColumns = [];
      expandedSchemas = {};
      selectedTable = null;
      clearQueryResults();
      tableResult = null;
      tableSort = null;
      clearResult();
      status = 'Disconnected';
    });
  }

  async function refresh(): Promise<void> {
    await perform('Loading tables…', async () => {
      availableSchemas = await Service.ListSchemas() ?? [];
      tables = await Service.ListTables() ?? [];
      await loadCatalogColumns();
      status = `${tables.length} tables and views`;
    });
  }

  async function browse(table: TableInfo, sort: SortState | null = null): Promise<void> {
    if (busy || !canLeaveRows()) return;
    mode = 'table';
    selectedTable = table;
    tableSort = sort;
    clearResult();
    tableEditInfo = null;
    await perform('Loading rows…', async () => {
      tableResult = normalizeResult(sort
        ? await Service.BrowseTableSorted(table.schema, table.name, sort.columnName, sort.descending)
        : await Service.BrowseTable(table.schema, table.name));
      try { tableEditInfo = await Service.GetTableEditInfo(table.schema, table.name); }
      catch (cause) { error = `Rows loaded, but editing is unavailable. ${message(cause)}`; }
      status = `${tableResult.rowCount} rows loaded`;
    });
  }

  async function run(statement: string): Promise<void> {
    if (!connection || busy || !canLeaveRows()) return;
    if (!statement.trim()) { error = 'Enter a SQL statement, or select the statement you want to run.'; return; }
    mode = 'sql';
    const panel = activeQuery;
    activeQuery.sort = null;
    clearResult();
    panel.editInfo = null;
    panel.error = '';
    panel.status = 'Running query…';
    runningQueryId = panel.id;
    await perform('Running query…', async () => {
      try {
        panel.result = normalizeResult(await Service.RunQuery(statement));
        const target = panel.result.deletionTarget;
        if (target) {
          try { panel.editInfo = await Service.GetTableEditInfo(target.schema, target.table); }
          catch (cause) { panel.error = `Rows loaded, but cell editing is unavailable. Run the query again to retry. ${message(cause)}`; }
        }
        panel.status = resultStatus(panel.result);
      } catch (cause) {
        panel.error = message(cause);
        panel.status = 'Query failed';
      } finally {
        runningQueryId = null;
        if (activeQueryId === panel.id && mode === 'sql') status = panel.status;
      }
    });
  }

  async function sortColumn(columnIndex: number): Promise<void> {
    if (!result || busy) return;
    const nextSort = {
      columnIndex,
      columnName: result.columns[columnIndex].name,
      descending: currentSort?.columnIndex === columnIndex ? !currentSort.descending : false,
    };
    if (mode === 'table' && selectedTable) await browse(selectedTable, nextSort);
    else activeQuery.sort = nextSort;
  }

  async function cancel(): Promise<void> {
    if (!canCancel || cancelling) return;
    cancelling = true;
    try { await Service.CancelQuery(); }
    catch (cause) { error = message(cause); cancelling = false; }
  }

  async function showSQL(): Promise<void> {
    if (mode === 'sql') { activeQuery.editor?.focus(); return; }
    if (!canLeaveRows()) return;
    mode = 'sql';
    selectedCell = null;
    resetSelection();
    await tick();
    activeQuery.editor?.focus();
  }

  async function showTable(): Promise<void> {
    if (mode === 'table' || busy || !selectedTable || !canLeaveRows()) return;
    if (!tableResult) { await browse(selectedTable, tableSort); return; }
    mode = 'table';
    selectedCell = null;
    resetSelection();
  }

  function closeRowMenu(restoreFocus = false): void {
    const trigger = rowMenu?.trigger;
    rowMenu = null;
    if (restoreFocus && trigger?.isConnected) trigger.focus();
  }

  async function openRowMenu(event: MouseEvent | KeyboardEvent, cells: (string | null)[], sourceIndex: number, rowNumber: number): Promise<void> {
    event.preventDefault();
    if (busy) return;
    const trigger = event.currentTarget as HTMLButtonElement;
    const pointer = event instanceof MouseEvent && event.type === 'contextmenu' && (event.clientX !== 0 || event.clientY !== 0);
    if (!selectedRows.includes(sourceIndex)) selectRow(sourceIndex, false, false);
    await showRowMenu(trigger, cells, sourceIndex, rowNumber, true, pointer ? { x: event.clientX, y: event.clientY } : undefined);
  }

  async function showRowMenu(trigger: HTMLButtonElement, cells: (string | null)[], sourceIndex: number, rowNumber: number, focus = true, position?: { x: number; y: number }): Promise<void> {
    if (busy || dragSelection || !selectedRows.length) return;
    const bounds = trigger.getBoundingClientRect();
    rowMenu = {
      cells: [...cells], sourceIndex, rowNumber, rowIndices: [...selectedRows], trigger,
      x: position?.x ?? bounds.right + 4,
      y: position?.y ?? bounds.bottom,
    };
    await tick();
    if (!rowMenu || !rowMenuElement) return;
    rowMenu.x = Math.max(8, Math.min(rowMenu.x, window.innerWidth - rowMenuElement.offsetWidth - 8));
    rowMenu.y = Math.max(8, Math.min(rowMenu.y, window.innerHeight - rowMenuElement.offsetHeight - 8));
    if (focus) rowCopyButton?.focus();
  }

  function showSelectedRowMenu(sourceIndex: number, focus = true): void {
    if (!selectedRows.includes(sourceIndex)) sourceIndex = selectedRows.at(-1) ?? -1;
    const rowNumber = displayedRows.findIndex(row => row.sourceIndex === sourceIndex) + 1;
    const trigger = gridScroll?.querySelector<HTMLButtonElement>(`[data-row-index="${sourceIndex}"]`);
    if (trigger && rowNumber) void showRowMenu(trigger, displayedRows[rowNumber - 1].cells, sourceIndex, rowNumber, focus);
    else closeRowMenu();
  }

  function selectAllRows(event: MouseEvent): void {
    selectedRows = selectedRows.length === displayedRows.length ? [] : displayedRows.map(row => row.sourceIndex);
    selectionAnchor = selectedRows[0] ?? null;
    selectedCell = null;
    closeRowMenu();
    if (selectedRows.length) void showRowMenu(event.currentTarget as HTMLButtonElement, displayedRows[0].cells, displayedRows[0].sourceIndex, 1);
  }

  function rowMenuKeydown(event: KeyboardEvent): void {
    if (event.key === 'Tab') { closeRowMenu(true); return; }
    if (event.shiftKey && ['ArrowDown', 'ArrowUp'].includes(event.key) && rowMenu) {
      rowNumberKeydown(event, rowMenu.cells, rowMenu.sourceIndex, rowMenu.rowNumber);
      return;
    }
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const items = Array.from(rowMenuElement?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)') ?? []);
    if (!items.length) return;
    const current = items.indexOf(document.activeElement as HTMLButtonElement);
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1
      : (current + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
    items[next].focus();
  }

  function rowNumberKeydown(event: KeyboardEvent, cells: (string | null)[], sourceIndex: number, rowNumber: number): void {
    if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) {
      void openRowMenu(event, cells, sourceIndex, rowNumber);
    } else if (event.shiftKey && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
      event.preventDefault();
      const order = displayedRows.map(row => row.sourceIndex);
      const position = Math.max(0, Math.min(order.length - 1, order.indexOf(sourceIndex) + (event.key === 'ArrowDown' ? 1 : -1)));
      selectionAnchor ??= sourceIndex;
      selectedRows = selectRowRange(order, selectionAnchor, order[position]);
      gridScroll?.querySelector<HTMLButtonElement>(`[data-row-index="${order[position]}"]`)?.focus();
      requestAnimationFrame(() => showSelectedRowMenu(order[position], false));
    }
  }

  function selectRow(sourceIndex: number, extend: boolean, toggle: boolean): void {
    if (busy) return;
    closeRowMenu();
    selectedCell = null;
    exportOpen = false;
    if (extend && selectionAnchor !== null) {
      selectedRows = selectRowRange(displayedRows.map(row => row.sourceIndex), selectionAnchor, sourceIndex);
    } else {
      selectedRows = toggle
        ? selectedRows.includes(sourceIndex) ? selectedRows.filter(index => index !== sourceIndex) : [...selectedRows, sourceIndex]
        : [sourceIndex];
      selectionAnchor = sourceIndex;
    }
  }

  function startRowSelection(event: PointerEvent, sourceIndex: number): void {
    if (event.button !== 0 || busy) return;
    event.preventDefault();
    (event.currentTarget as HTMLButtonElement).focus();
    stopRowSelection();
    const base = event.metaKey || event.ctrlKey ? [...selectedRows] : [];
    const remove = (event.metaKey || event.ctrlKey) && selectedRows.includes(sourceIndex);
    selectRow(sourceIndex, event.shiftKey, event.metaKey || event.ctrlKey);
    dragSelection = { anchor: selectionAnchor ?? sourceIndex, target: sourceIndex, base, remove, startY: event.clientY, y: event.clientY, moved: false, frame: 0 };
    dragSelection.frame = requestAnimationFrame(continueRowSelection);
  }

  function continueRowSelection(): void {
    if (!dragSelection || !gridScroll) return;
    if (!dragSelection.moved) { dragSelection.frame = requestAnimationFrame(continueRowSelection); return; }
    const bounds = gridScroll.getBoundingClientRect();
    const headerHeight = gridScroll.querySelector('thead')?.getBoundingClientRect().height ?? 40;
    const top = bounds.top + headerHeight;
    const bottom = bounds.bottom - 16;
    if (dragSelection.y < top + 20) gridScroll.scrollTop -= 12;
    else if (dragSelection.y > bottom - 20) gridScroll.scrollTop += 12;
    const target = document.elementFromPoint(bounds.left + 20, Math.max(top + 1, Math.min(bottom - 1, dragSelection.y)))?.closest<HTMLButtonElement>('[data-row-index]');
    if (target) {
      dragSelection.target = Number(target.dataset.rowIndex);
      const range = selectRowRange(displayedRows.map(row => row.sourceIndex), dragSelection.anchor, dragSelection.target);
      selectedRows = dragSelection.remove ? dragSelection.base.filter(index => !range.includes(index)) : [...new Set([...dragSelection.base, ...range])];
    }
    dragSelection.frame = requestAnimationFrame(continueRowSelection);
  }

  function stopRowSelection(): void {
    if (dragSelection) cancelAnimationFrame(dragSelection.frame);
    dragSelection = null;
  }

  function finishRowSelection(): void {
    if (!dragSelection) return;
    if (dragSelection.moved) {
      cancelAnimationFrame(dragSelection.frame);
      continueRowSelection();
    }
    const sourceIndex = dragSelection.target;
    stopRowSelection();
    requestAnimationFrame(() => showSelectedRowMenu(sourceIndex));
  }

  async function toggleExport(): Promise<void> {
    exportOpen = !exportOpen;
    if (exportOpen) { await tick(); exportCSVButton?.focus(); }
  }

  async function exportRows(): Promise<void> {
    if (!result || !selectedRows.length || busy) return;
    const rows = displayedRows.filter(row => selectedRows.includes(row.sourceIndex)).map(row => row.cells);
    const csv = rowsToCSV(rows, result.columns.map(column => column.name));
    const filename = `${mode === 'table' ? selectedTable?.name ?? 'rows' : 'query-results'}.csv`;
    exportOpen = false;
    exportButton?.focus();
    await perform('Exporting rows…', async () => {
      if (location.protocol === 'http:' || location.protocol === 'https:') {
        // Wails server mode uses the browser's download destination.
        const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }));
        const link = document.createElement('a');
        link.href = url;
        link.download = filename;
        document.body.append(link);
        link.click();
        link.remove();
        setTimeout(() => URL.revokeObjectURL(url), 1000);
      } else if (!await ExportService.SaveCSV(filename, csv)) {
        status = 'Export cancelled';
        return;
      }
      status = `Exported ${rows.length} ${rows.length === 1 ? 'row' : 'rows'} as CSV with column headers`;
    });
  }

  async function openCell(sourceIndex: number, columnIndex: number, value: string | null): Promise<void> {
    if (!result || busy) return;
    selectedCell = { column: result.columns[columnIndex].name, type: result.columns[columnIndex].dataType, value, row: sourceIndex, columnIndex };
    resetSelection();
    await tick();
    if (cellEditor?.disabled) cellNullToggle?.focus();
    else cellEditor?.focus();
  }

  function closeCellDetails(): void {
    if (selectedCell) gridScroll?.querySelector<HTMLButtonElement>(`[data-cell-row="${selectedCell.row}"][data-column-index="${selectedCell.columnIndex}"]`)?.focus();
    selectedCell = null;
  }

  function editCell(value: string | null): void {
    if (!editableCell || !selectedCell || !result || busy) return;
    const edits = updateCellEdit(activeCellEdits, selectedCell.row, sourceColumns[selectedCell.columnIndex], value, result.rows[selectedCell.row][selectedCell.columnIndex]);
    if (mode === 'sql') activeQuery.cellEdits = edits;
    else cellEdits = edits;
    selectedCell.value = value;
    status = pendingChanges.length ? 'Unsaved row changes' : 'No unsaved changes';
  }

  function removeRows(indices: number[]): void {
    if (!canRemoveRows || !result || busy) return;
    const rows = [...new Set(indices)].filter(index => result!.rows[index] && !activeDeletedRows.includes(index));
    if (!rows.length) return;
    closeRowMenu(true);
    if (mode === 'sql') activeQuery.deletedRows = [...activeQuery.deletedRows, ...rows];
    else deletedRows = [...deletedRows, ...rows];
    selectedCell = null;
    resetSelection();
    status = rows.length === 1 ? 'Row marked for removal. Save changes to remove it from the database.'
      : `${rows.length} rows marked for removal. Save changes to remove them from the database.`;
  }

  function undoChanges(): void {
    if (busy) return;
    if (mode === 'sql') {
      activeQuery.cellEdits = {};
      activeQuery.deletedRows = [];
    }
    else {
      cellEdits = {};
      deletedRows = [];
    }
    if (selectedCell && result) selectedCell.value = result.rows[selectedCell.row][selectedCell.columnIndex];
    error = '';
    status = 'Row changes undone';
  }

  async function saveRowChanges(): Promise<void> {
    if (busy || !pendingChanges.length) return;
    if (mode === 'table') { await saveTableChanges(); return; }
    const source = activeQuery.result;
    const target = source?.deletionTarget;
    if (!source || !target?.columns) return;
    const hasEdits = Object.keys(activeQuery.cellEdits).length > 0;
    await perform('Saving row changes…', async () => {
      const count = await Service.SaveTableChanges(target.schema, target.table, target.columns!, queryPendingChanges);
      const rows = applyRowChanges(source.rows, target.columns!, activeQuery.cellEdits, activeQuery.deletedRows);
      // SQL may contain writes or side effects; update the loaded result without executing it again.
      activeQuery.result = { ...source, rows, rowCount: rows.length };
      activeQuery.cellEdits = {};
      activeQuery.deletedRows = [];
      selectedCell = null;
      resetSelection();
      if (selectedTable?.schema === target.schema && selectedTable.name === target.table) {
        tableResult = null;
        tableEditInfo = null;
      }
      status = hasEdits ? `Saved changes to ${count} ${count === 1 ? 'row' : 'rows'}. Run the query again to refresh database-generated values.`
        : `Removed ${count} ${count === 1 ? 'row' : 'rows'}`;
      activeQuery.status = status;
    });
  }

  async function saveTableChanges(): Promise<void> {
    if (!selectedTable || !tableResult || !pendingChanges.length || busy) return;
    const table = selectedTable;
    await perform('Saving table changes…', async () => {
      const count = await Service.SaveTableChanges(table.schema, table.name, tableResult!.columns.map(column => column.name), pendingChanges);
      cellEdits = {};
      deletedRows = [];
      selectedCell = null;
      resetSelection();
      tableResult = null;
      status = `Saved changes to ${count} ${count === 1 ? 'row' : 'rows'}`;
      try {
        tableResult = normalizeResult(tableSort
          ? await Service.BrowseTableSorted(table.schema, table.name, tableSort.columnName, tableSort.descending)
          : await Service.BrowseTable(table.schema, table.name));
      } catch (cause) { error = `Changes were saved, but rows could not be refreshed. Refresh rows to try again. ${message(cause)}`; }
    });
  }

  async function writeClipboard(text: string): Promise<void> {
    if (location.protocol === 'http:' || location.protocol === 'https:') await navigator.clipboard.writeText(text);
    else await Clipboard.SetText(text);
  }

  async function copyCell(): Promise<void> {
    if (!selectedCell) return;
    try {
      await writeClipboard(selectedCell.value ?? 'NULL');
      status = 'Copied cell value';
    } catch (cause) {
      error = `Could not copy the cell. ${message(cause)}`;
      status = 'Copy failed';
    }
  }

  async function copyRow(): Promise<void> {
    const row = rowMenu;
    if (!row) return;
    closeRowMenu(true);
    try {
      const csv = rowsToCSV([row.cells]);
      await writeClipboard(csv);
      status = `Copied row ${row.rowNumber} as CSV`;
    } catch (cause) {
      error = `Could not copy the row. ${message(cause)}`;
      status = 'Copy failed';
    }
  }

  function windowKeydown(event: KeyboardEvent): void {
    if (pendingCloseQuery) return;
    if (selectedCell && !event.defaultPrevented && (event.metaKey || event.ctrlKey) && !event.altKey && event.key.toLowerCase() === 'c') {
      const target = event.target;
      if (target === cellEditor) {
        if (cellEditor && cellEditor.selectionStart !== cellEditor.selectionEnd) return;
      } else if (target instanceof Element && target !== cellNullToggle && target.closest('input, textarea, [contenteditable]')) return;
      if (window.getSelection()?.toString()) return;
      event.preventDefault();
      void copyCell();
      return;
    }
    if (event.key !== 'Escape') return;
    if (exportOpen) { event.preventDefault(); exportOpen = false; exportButton?.focus(); }
    else if (rowMenu) { event.preventDefault(); closeRowMenu(true); }
    else if (selectedCell) closeCellDetails();
    else resetSelection();
  }

  function cellText(value: string | null): string { return value === null ? 'NULL' : value === '' ? '""' : value; }
</script>

<svelte:window bind:innerWidth={windowWidth} bind:innerHeight={windowHeight} onkeydown={windowKeydown} onpointerdown={event => { if (rowMenu && event.target instanceof Node && !rowMenuElement?.contains(event.target)) closeRowMenu(); if (exportOpen && event.target instanceof Element && !event.target.closest('.export-control')) exportOpen = false; }} onpointermove={event => { if (dragSelection) { dragSelection.y = event.clientY; if (Math.abs(event.clientY - dragSelection.startY) >= 3) dragSelection.moved = true; } }} onpointerup={finishRowSelection} onpointercancel={stopRowSelection} onblur={() => { closeRowMenu(); stopRowSelection(); }} onresize={() => { closeRowMenu(); requestAnimationFrame(scrollActiveQueryIntoView); }} />

{#snippet resultMetrics()}
  {#if result}
    <span class="result-counts">{#if result.columns.length}{result.rowCount.toLocaleString()} rows<span class="separator">·</span>{/if}{result.durationMs} ms{#if hasAffectedRows(result.commandTag)}<span class="separator">·</span>{result.rowsAffected.toLocaleString()} affected{/if}</span>
  {/if}
{/snippet}

{#snippet connectionEditor()}
    <section class="connection-view" aria-labelledby="connect-title" aria-busy={!!busy}>
      <div class="connection-view-heading"><div><h1 id="connect-title">{editingSavedId ? 'Edit connection' : 'New connection'}</h1><p>{editingSavedId ? 'Update the saved name, color, and connection settings.' : 'Connect to PostgreSQL or open a SQLite database.'}</p></div></div>
        <form class="connection-form" onsubmit={event => { event.preventDefault(); void (editingSavedId ? saveEditedConnection() : connectionName.trim() ? saveAndConnect() : connect()); }} autocomplete="off">
          <div class="connection-scroll">
            <div class="connection-editor">
              <div class="connection-identity">
                <div class="connection-field provider-field">
                  <label for="connection-provider">Database type</label>
                  <select id="connection-provider" bind:value={connectionProvider} disabled={!!busy || !!editingSavedId} onchange={() => { error = ''; }}>
                    <option value="postgres">PostgreSQL</option>
                    <option value="sqlite">SQLite</option>
                  </select>
                </div>
                <div class="connection-field">
                  <label for="connection-name">Connection name {#if !editingSavedId}<span class="optional-label">optional</span>{/if}</label>
                  <input id="connection-name" type="text" bind:value={connectionName} required={!!editingSavedId} maxlength="100" placeholder="e.g. Local development" autocomplete="off" disabled={!!busy} aria-describedby="connection-name-help" />
                  <p id="connection-name-help" class="field-hint">{editingSavedId ? 'Settings apply the next time you connect.' : 'Leave blank for a temporary connection.'}</p>
                </div>
                <fieldset class="connection-colors" disabled={!!busy}><legend>Connection color</legend><div>{#each connectionColors as color}<button type="button" class="color-choice" class:chosen={connectionColor === color.value} style:--choice-color={color.value || '#999999'} aria-label={`${color.name} connection color`} aria-pressed={connectionColor === color.value} onclick={() => { connectionColor = color.value; }} title={color.name}><span aria-hidden="true">{color.value ? '' : '∕'}</span></button>{/each}</div></fieldset>
              </div>
              <div class="connection-modes" role="group" aria-label="Connection input format">
                {#if connectionProvider === 'sqlite'}
                <button type="button" class="active" aria-pressed="true" disabled={!!busy}>Database file</button>
                {:else}
                <button type="button" class:active={connectionMode === 'fields'} aria-pressed={connectionMode === 'fields'} disabled={!!busy || !!editingSavedId} onclick={() => { connectionMode = 'fields'; error = ''; }}>Connection details</button>
                <button type="button" class:active={connectionMode === 'url'} aria-pressed={connectionMode === 'url'} disabled={!!busy || !!editingSavedId} onclick={() => { connectionMode = 'url'; error = ''; }}>Connection URL</button>
                {/if}
              </div>
              <div class="connection-method">
              {#if connectionProvider === 'sqlite'}
                <div class="connection-field">
                  <label for="connection-file">Database file path</label>
                  <input id="connection-file" type="text" bind:value={connectionFilePath} placeholder="/Users/you/data/app.sqlite3" required autocomplete="off" autocapitalize="none" spellcheck="false" disabled={!!busy} aria-describedby="connection-file-help" />
                </div>
                <p id="connection-file-help" class="field-hint connection-help">Enter the absolute path to an existing SQLite file. Queries can change the data in this file.</p>
              {:else if connectionMode === 'fields'}
                <div class="connection-fields">
                  <div class="connection-address">
                    <div class="connection-field">
                      <label for="connection-host">Host</label>
                      <input id="connection-host" type="text" bind:value={connectionHost} placeholder="db.example.com" required autocapitalize="none" spellcheck="false" disabled={!!busy} />
                    </div>
                    <div class="connection-field">
                      <label for="connection-port">Port</label>
                      <input id="connection-port" type="number" bind:value={connectionPort} required min="1" max="65535" step="1" disabled={!!busy} />
                    </div>
                  </div>
                  <div class="connection-field">
                    <label for="connection-database">Database</label>
                    <input id="connection-database" type="text" bind:value={connectionDatabase} required autocapitalize="none" spellcheck="false" disabled={!!busy} />
                  </div>
                  <div class="connection-field">
                    <label for="connection-user">Username</label>
                    <input id="connection-user" type="text" bind:value={connectionUser} placeholder="Your database username" required autocapitalize="none" spellcheck="false" disabled={!!busy} />
                  </div>
                  <div class="connection-field">
                    <label for="connection-password">Password {#if !editingSavedId}<span class="optional-label">if required</span>{/if}</label>
                    <input id="connection-password" type="password" bind:value={connectionPassword} placeholder={editingSavedId && !replacePassword ? 'Saved password kept' : 'Database password'} autocomplete="off" disabled={!!busy || (!!editingSavedId && !replacePassword)} />
                    {#if editingSavedId}<label class="password-toggle"><input type="checkbox" bind:checked={replacePassword} disabled={!!busy} />Replace saved password</label>{#if replacePassword}<p class="field-hint">Leave empty to remove the password.</p>{/if}{/if}
                  </div>
                  <div class="connection-field">
                    <label for="connection-tls">SSL mode</label>
                    <select id="connection-tls" bind:value={connectionSSLMode} disabled={!!busy}>
                      <option value="prefer">Prefer (default)</option>
                      <option value="require">Require</option>
                      <option value="verify-ca">Verify CA</option>
                      <option value="verify-full">Verify full</option>
                      <option value="disable">Disable</option>
                    </select>
                  </div>
                </div>
                <p class="field-hint connection-help">Connecting locally? The host is usually <code>localhost</code> and the port <code>5432</code>. Use a URL for certificate options.</p>
              {:else}
                <div class="connection-field">
                  <label for="connection-url">PostgreSQL connection URL</label>
                  <input id="connection-url" type="password" bind:value={connectionURL} placeholder={editingSavedId ? 'Leave blank to keep the saved URL' : 'postgresql://user:password@host/database'} required={!editingSavedId} autocomplete="off" autocapitalize="none" spellcheck="false" disabled={!!busy} aria-describedby="connection-url-help" />
                </div>
                <p id="connection-url-help" class="field-hint connection-help">{editingSavedId ? 'Leave blank to keep the saved URL, or paste a complete replacement.' : 'Paste the URL from your provider. PostgreSQL and JDBC URLs are supported.'} Include <code>?sslmode=prefer</code> (or your provider’s SSL mode) to save it.</p>
              {/if}
              </div>
              <div class="connection-privacy"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><rect x="5" y="10" width="14" height="11" rx="2" /><path d="M8 10V7a4 4 0 0 1 8 0v3M12 14v3" /></svg><span>Saved credentials are encrypted with macOS Keychain protection.</span></div>
            </div>
          </div>
          {#if error || queryStorageError}<div class="connection-error" role="alert"><p>{error || queryStorageError}</p></div>{/if}
          <div class="connect-actions">
            <div class="connect-actions-inner">
            <span class="connection-feedback" role="status">{#if busy}<span class="spinner"></span>{busy}{:else}{editingSavedId ? connection ? 'Your current session stays open.' : 'Save without connecting.' : 'Connect to browse tables and run SQL.'}{/if}</span>
            {#if editingSavedId}<button type="button" disabled={!!busy} onclick={cancelEdit}>Cancel</button>{/if}
            {#if canCancel}<button type="button" class="cancel-connect" disabled={cancelling} onclick={cancel}>{cancelling ? 'Cancelling…' : 'Cancel'}</button>{/if}
            <button class="primary connect-button" type="submit" disabled={!!busy || !canConnect || (!!editingSavedId && !connectionName.trim()) || (!!connectionName.trim() && savedLoading)}>
              {editingSavedId ? 'Save changes' : connectionName.trim() ? 'Save & connect' : 'Connect'}
            </button>
            </div>
          </div>
        </form>
    </section>
{/snippet}

{#snippet savedConnectionList()}
  <div class="section-heading saved-heading">
    <span class="section-label">Saved connections</span>
    <button class="icon-button" onclick={loadSavedConnections} disabled={!!busy || savedLoading} aria-label="Refresh saved connections" title="Refresh saved connections">↻</button>
  </div>
  {#if savedLoading}<p class="saved-note" role="status">Loading connections…</p>{/if}
  {#if storageError}
    <div class="saved-error" role="alert"><p>{storageError}</p><button type="button" onclick={loadSavedConnections} disabled={!!busy || savedLoading}>Retry</button></div>
  {/if}
  {#if savedConnections.length > 5 || savedSearch}<input class="saved-search" type="search" bind:value={savedSearch} aria-label="Search saved connections" placeholder="Find a connection…" />{/if}
  <ul class="saved-connections" aria-label="Saved connections">
    {#each visibleSavedConnections as saved (saved.id)}
      <li class:active={activeSavedId === saved.id} style:--connection-color={saved.color || '#777777'} class:color-coded={!!saved.color} class:editing={editingSavedId === saved.id}>
        <div class="saved-row">
          <button class="saved-connect" disabled={!!busy || savedLoading} onclick={() => connectSaved(saved)} aria-label={`Connect to ${saved.name}`} title={saved.provider === 'sqlite' ? connectionEndpoint(saved) : `${saved.user}@${saved.host}:${saved.port}/${saved.database}`}>
            <span class="saved-icon" aria-hidden="true"><DatabaseIcon provider={saved.provider} /></span>
            <span class="saved-info">
              <span class="saved-name-line"><span class="saved-name">{saved.name}</span>{#if activeSavedId === saved.id}<span class="saved-state">{activeSettingsChanged ? 'Reconnect' : 'Active'}</span>{/if}</span>
              <span class="saved-database">{saved.database}</span>
              <span class="saved-target">{connectionEndpoint(saved)}</span>
            </span>
          </button>
          <div class="saved-row-actions">
            <button class="icon-button saved-edit" disabled={!!busy || savedLoading} onclick={() => startEdit(saved)} aria-label={`Edit saved connection ${saved.name}`} title="Edit saved connection"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><path d="m15 4 5 5M4 15 15 4a3.5 3.5 0 0 1 5 5L9 20l-6 1 1-6Z" /></svg></button>
          <button class="icon-button saved-delete" disabled={!!busy || savedLoading} onclick={() => { pendingDeleteId = pendingDeleteId === saved.id ? null : saved.id; }} aria-label={`Delete saved connection ${saved.name}`} title="Delete saved connection"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><path d="M4 7h16M9 7V4h6v3M6 7l1 14h10l1-14M10 10v7M14 10v7" /></svg></button>
          </div>
        </div>
        {#if pendingDeleteId === saved.id}
          <div class="saved-confirm" role="group" aria-label={`Confirm deleting ${saved.name}`}>
            <span>Delete this saved connection?</span>
            <div><button type="button" disabled={!!busy || savedLoading} onclick={() => deleteSaved(saved)}>Delete</button><button type="button" disabled={!!busy} onclick={() => { pendingDeleteId = null; }}>Keep</button></div>
          </div>
        {/if}
      </li>
    {/each}
  </ul>
  {#if savedSearch && savedConnections.length && !visibleSavedConnections.length}<p class="saved-note">No matching connections.</p>{/if}
  {#if !savedLoading && !storageError && !savedConnections.length}<p class="saved-note saved-empty">{connection ? 'Disconnect to add a new connection and save it for next time.' : 'Name a connection to save it here for next time.'}</p>{/if}
{/snippet}

<div class="app-shell" style:--sidebar-width={`${visibleSidebarWidth}px`}>
  <aside class="sidebar" aria-label="Database navigation">
    <div class="brand"><img class="brand-icon" src="/tusk-icon.png" alt="" aria-hidden="true" /><strong>Tusk</strong><span class="brand-subtitle">{connection?.provider === 'sqlite' ? 'SQLite' : connection ? 'PostgreSQL' : 'Databases'}</span></div>
    {#if connection}
    <section class="session-section" aria-label="Active connection" style:--connection-color={activeConnectionColor} style:height={savedSwitcherOpen ? `${visibleConnectionsHeight}px` : undefined}>
      <div class="session-heading"><span class="dot connected" style:background={activeConnectionColor}></span><strong title={connection.database}>{savedConnections.find(saved => saved.id === activeSavedId)?.name ?? connection.database}</strong><button class="quiet small" disabled={!!busy} onclick={disconnect}>Disconnect</button></div>
      <p class="session-database" title={connectionEndpoint(connection)}>{connection.database}<span class="session-endpoint">{connectionEndpoint(connection)}</span></p>
      {#if activeSettingsChanged}<p class="session-settings-note">Reconnect to apply saved settings.</p>{/if}
      <div class="saved-switcher">
        <button class="saved-switcher-toggle" aria-expanded={savedSwitcherOpen} aria-controls="saved-switcher-list" onclick={() => { savedSwitcherOpen = !savedSwitcherOpen; }}>Switch connection<span class="count">{savedConnections.length}</span></button>
        {#if savedSwitcherOpen}<div class="saved-switcher-list" id="saved-switcher-list">{@render savedConnectionList()}</div>{/if}
      </div>
    </section>
    {:else}
    <section class="connection-section" style:height={`${visibleConnectionsHeight}px`}>{@render savedConnectionList()}</section>
    {/if}
    {#if !connection || savedSwitcherOpen}<PanelDivider label="Resize connections panel" orientation="horizontal" value={visibleConnectionsHeight} min={240} max={maxConnectionsHeight} onresize={value => { connectionsHeight = value; }} />{/if}
    {#if connection}
    <section class="tables-section">
      <div class="section-heading"><span class="section-label">Database objects</span><button class="icon-button" onclick={refresh} disabled={!connection || !!busy} aria-label="Refresh tables" title="Refresh tables">↻</button></div>
      <input class="search" type="search" aria-label="Search schemas and tables" placeholder="Search tables…" bind:value={search} disabled={!connection} />
      <nav class="table-tree" aria-label="Schemas and tables">
        {#each schemas as schema (schema)}
          <details open={!!searchTerm || expandedSchemas[schema] !== false}>
            <summary onclick={event => toggleSchema(event, schema)}><span class="schema-symbol" aria-hidden="true">▱</span>{schema}<span class="count">{filteredTables.filter(table => table.schema === schema).length}</span></summary>
            {#each filteredTables.filter(table => table.schema === schema) as table (`${table.schema}\u0000${table.name}`)}
              <button class:active={mode === 'table' && selectedTable?.schema === table.schema && selectedTable?.name === table.name} aria-current={mode === 'table' && selectedTable?.schema === table.schema && selectedTable?.name === table.name ? 'page' : undefined} class="table-item" disabled={!!busy} onclick={() => browse(table)} title={`${table.schema}.${table.name} · ${table.type}`}><span class="table-symbol" aria-hidden="true">▦</span><span class="table-name">{table.name}</span>{#if table.type.toLowerCase().includes('view')}<span class="object-type">view</span>{/if}</button>
            {/each}
            {#if !filteredTables.some(table => table.schema === schema)}<p class="schema-empty">No tables</p>{/if}
          </details>
        {/each}
        {#if !schemas.length}<p class="sidebar-empty">{search ? 'No matching schemas or tables.' : 'No accessible schemas.'}</p>{/if}
      </nav>
    </section>
    {:else}
    <div class="connection-navigation"><button class="new-connection" disabled={!!busy} class:active={!editingSavedId} onclick={() => { if (editingSavedId) cancelEdit(); document.getElementById('connection-name')?.focus(); }} aria-current="page"><span aria-hidden="true">＋</span>New connection</button></div>
    {/if}
    <div class="sidebar-footer"><span class="dot" class:connected={!!connection}></span>{connection ? 'Connected' : 'No active connection'}</div>
  </aside>
  <PanelDivider label="Resize sidebar" orientation="vertical" value={visibleSidebarWidth} min={220} max={maxSidebarWidth} onresize={value => { sidebarWidth = value; }} />

  <main class="workspace">
    {#if !connection}
    <header class="connection-toolbar"><span>Connections</span><span class="toolbar-note">PostgreSQL & SQLite</span></header>
    {@render connectionEditor()}
    {:else}
    <header class="workspace-toolbar">
      <nav class="workspace-views" aria-label="Workspace views">
        <button class:active={mode === 'sql'} aria-pressed={mode === 'sql'} onclick={showSQL}><span aria-hidden="true">⌘</span>SQL editor</button>
        <button class:active={mode === 'table'} aria-pressed={mode === 'table'} disabled={!selectedTable} onclick={showTable}><span aria-hidden="true">▦</span>Table browser</button>
      </nav>
      <div class="workspace-actions" role="group" aria-label="Workspace actions">
        <button disabled={!canCancel || cancelling} onclick={cancel}>{cancelling ? 'Cancelling…' : 'Cancel'}</button>
        {#if mode === 'sql'}
          <button class="primary workspace-primary" disabled={!!busy || !activeQuery.text.trim()} onclick={() => run(activeQuery.editor?.getQuery() ?? activeQuery.text)} title="Run the selection or the entire editor (⌘ Enter)"><span aria-hidden="true">▶</span>Run query</button>
        {:else}
          <button class="primary workspace-primary" disabled={!!busy || !selectedTable} onclick={() => browse(selectedTable!, tableSort)}><span aria-hidden="true">↻</span>Refresh rows</button>
        {/if}
      </div>
    </header>
    <div class="workspace-context">
      <div class="context-heading">
        <h1 title={mode === 'sql' ? `Query ${activeQueryNumber}` : selectedTable?.name}>{mode === 'sql' ? `Query ${activeQueryNumber}` : selectedTable?.name}</h1>
        <p>{mode === 'sql' ? connection.provider === 'sqlite' ? 'Runs against the database file, including writes.' : 'Runs with your account’s permissions, including writes.' : `${selectedTable?.schema} / ${selectedTable?.type}`}</p>
      </div>
      {#if selectedRows.length || pendingChanges.length}
      <div class="grid-actions" role="group" aria-label="Row actions">
        <span class="selection-summary" role="status">{pendingChanges.length ? `${pendingChanges.length} ${pendingChanges.length === 1 ? 'row' : 'rows'} changed` : `${selectedRows.length} selected`}</span>
        {#if selectedRows.length}
          <div class="export-control">
            <button type="button" disabled={!!busy} aria-haspopup="menu" aria-expanded={exportOpen} onclick={toggleExport} bind:this={exportButton}>Export <span aria-hidden="true">⌄</span></button>
            {#if exportOpen}
              <div class="export-menu" role="menu" aria-label="Export format" tabindex="-1" onkeydown={event => { if (event.key === 'Tab') exportOpen = false; else if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) { event.preventDefault(); exportCSVButton?.focus(); } }}><button type="button" role="menuitem" bind:this={exportCSVButton} onclick={exportRows}>CSV with headers (.csv)</button></div>
            {/if}
          </div>
          {#if canRemoveRows && selectedRows.length > 1 && selectedRows.some(index => !activeDeletedRows.includes(index))}<button type="button" disabled={!!busy} onclick={() => removeRows(selectedRows)}>Remove rows</button>{/if}
        {/if}
        {#if pendingChanges.length}
          <button type="button" disabled={!!busy} onclick={undoChanges}>Undo</button>
          <button type="button" class="primary" disabled={!!busy} onclick={saveRowChanges}>Save changes</button>
        {/if}
      </div>
      {:else}<div class="context-summary" role="status">
        {#if mode === 'sql'}<span class="keyboard-hint"><kbd>⌘</kbd><kbd>Enter</kbd> to run</span>
        {:else}{#if busy}<span class="running"><span class="spinner"></span>{busy}</span>{:else}{@render resultMetrics()}{/if}<span class="context-note">First 100 rows{#if tableSort} · Sorted {tableSort.descending ? 'descending' : 'ascending'}{/if}</span>{/if}
      </div>{/if}
    </div>
    <div class="query-tabs-bar" class:hidden={mode !== 'sql'}>
      <div class="query-tabs" role="tablist" aria-label="Queries">
        {#each queryPanels as panel, index (panel.id)}
          <div class="query-tab" class:active={panel.id === activeQueryId}>
            <button type="button" role="tab" id={`query-tab-${panel.id}`} aria-controls={`query-panel-${panel.id}`} aria-selected={panel.id === activeQueryId} tabindex={panel.id === activeQueryId ? 0 : -1} disabled={!!busy && runningQueryId === null} onclick={() => selectQuery(panel)} onkeydown={event => queryTabKeydown(event, index)}>{#if runningQueryId === panel.id}<span class="spinner" aria-label="Running query"></span>{/if}Query {index + 1}{#if panel.deletedRows.length || Object.keys(panel.cellEdits).length}<span class="query-changes" aria-label="Unsaved row changes">•</span>{/if}</button>
            {#if queryPanels.length > 1}<button type="button" class="query-close" disabled={!!busy} aria-label={`Close Query ${index + 1}`} title={`Close Query ${index + 1}`} onclick={() => closeQuery(panel)}>×</button>{/if}
          </div>
        {/each}
      </div>
      <button type="button" class="query-add" disabled={!!busy && runningQueryId === null} aria-label="New query" title="New query" onclick={addQuery}>＋</button>
    </div>
    {#each queryPanels as panel (panel.id)}
      <div class="query-section" class:hidden={mode !== 'sql' || panel.id !== activeQueryId} id={`query-panel-${panel.id}`} role="tabpanel" aria-labelledby={`query-tab-${panel.id}`} style:height={`${visibleEditorHeight}px`}>
        <div class="editor"><SqlEditor provider={connection.provider} tables={tables} columns={catalogColumns} bind:this={panel.editor} bind:value={panel.text} disabled={!connection || !!busy} onrun={run} /></div>
      </div>
    {/each}
    {#if mode === 'sql'}<PanelDivider label="Resize SQL editor" orientation="horizontal" value={visibleEditorHeight} min={150} max={maxEditorHeight} onresize={value => { editorHeight = value; }} />{/if}
    {#if workspaceError}<div class="error-banner" role="alert"><span aria-hidden="true">!</span><div>{workspaceError}</div>{#if error || (mode === 'sql' && activeQuery.error)}<button aria-label="Dismiss error" onclick={() => { error = ''; activeQuery.error = ''; }}>×</button>{/if}</div>{/if}

    <div class="results-layout">
      <section class="results" aria-label="Query results" aria-busy={!!busy}>
        {#if mode === 'sql'}
          <div class="results-toolbar"><span class="section-label">Results</span>{@render resultMetrics()}{#if currentSort}<span class="sort-scope">Displayed rows · {currentSort.descending ? 'descending' : 'ascending'}</span>{/if}{#if busy}<span class="running"><span class="spinner"></span>{busy}</span>{/if}</div>
        {/if}
        {#if result && result.columns.length}
          <!-- svelte-ignore a11y_no_noninteractive_tabindex (The scroll region needs keyboard focus to reach off-screen columns.) -->
          <div class="grid-scroll" tabindex="0" role="region" aria-label="Scrollable result grid" bind:this={gridScroll} onscroll={() => closeRowMenu()}>
            <table class="data-grid">
              <thead>
                <tr>
                  <th class="row-number"><button type="button" class="row-number-button" data-select-all="true" disabled={!!busy || !displayedRows.length} aria-label="Select all displayed rows" aria-pressed={!!displayedRows.length && selectedRows.length === displayedRows.length} aria-haspopup="menu" aria-expanded={rowMenu?.trigger.dataset.selectAll === 'true'} title="Select all displayed rows" onclick={selectAllRows}>#</button></th>
                  {#each result.columns as column, columnIndex}
                    <th scope="col" aria-sort={currentSort?.columnIndex === columnIndex ? currentSort.descending ? 'descending' : 'ascending' : 'none'}>
                      <button class="column-sort" disabled={!!busy} onclick={() => sortColumn(columnIndex)} title={mode === 'sql' ? 'Sort displayed rows only' : 'Sort the table before fetching its first 100 rows'}>
                        <span>{column.name}<span class="sort-indicator" aria-hidden="true">{currentSort?.columnIndex === columnIndex ? currentSort.descending ? ' ↓' : ' ↑' : ''}</span></span>
                        <span class="column-type">{column.dataType}</span>
                      </button>
                    </th>
                  {/each}
                </tr>
              </thead>
              <tbody>
                {#each displayedRows as row, rowIndex (row.sourceIndex)}
                  <tr class:row-selected={selectedRows.includes(row.sourceIndex)} class:row-deleted={activeDeletedRows.includes(row.sourceIndex)}>
                    <th class="row-number" scope="row"><button type="button" class="row-number-button" disabled={!!busy} data-row-index={row.sourceIndex} aria-label={`Select row ${rowIndex + 1}${activeDeletedRows.includes(row.sourceIndex) ? ', marked for removal' : ''}`} aria-pressed={selectedRows.includes(row.sourceIndex)} aria-haspopup="menu" aria-expanded={rowMenu?.trigger.dataset.rowIndex === String(row.sourceIndex)} aria-controls={rowMenu?.trigger.dataset.rowIndex === String(row.sourceIndex) ? 'row-context-menu' : undefined} title="Click or drag to select rows and show row actions; Shift-click extends, ⌘-click toggles." onpointerdown={event => startRowSelection(event, row.sourceIndex)} onclick={event => { if (event.detail === 0) { selectRow(row.sourceIndex, event.shiftKey, event.metaKey || event.ctrlKey); showSelectedRowMenu(row.sourceIndex); } }} oncontextmenu={event => { void openRowMenu(event, row.cells, row.sourceIndex, rowIndex + 1); }} onkeydown={event => rowNumberKeydown(event, row.cells, row.sourceIndex, rowIndex + 1)}>{rowIndex + 1}</button></th>
                    {#each row.cells as value, columnIndex}
                      <td><button class="cell" data-cell-row={row.sourceIndex} data-column-index={columnIndex} disabled={!!busy} class:null-value={value === null} class:empty-value={value === ''} class:cell-changed={Object.hasOwn(activeCellEdits[row.sourceIndex] ?? {}, sourceColumns[columnIndex])} class:selected={selectedCell?.row === row.sourceIndex && selectedCell?.columnIndex === columnIndex} onclick={() => openCell(row.sourceIndex, columnIndex, value)} aria-label={`Row ${rowIndex + 1}, ${result.columns[columnIndex].name}: ${cellText(value)}`} title={activeDeletedRows.includes(row.sourceIndex) ? 'Marked for removal; undo to edit' : canEditRows && !activeEditInfo?.readOnlyColumns?.includes(sourceColumns[columnIndex]) ? 'Edit cell' : 'Open cell details'}>{cellText(value)}</button></td>
                    {/each}
                  </tr>
                {/each}
              </tbody>
            </table>
            {#if !result.rows.length}<div class="empty-result">The query returned no rows.</div>{/if}
          </div>
        {:else if result}
          <div class="empty-state"><span class="empty-icon success" aria-hidden="true">✓</span><h2>Statement completed</h2><p>{resultStatus(result)}</p></div>
        {:else if busy}
          <div class="empty-state"><span class="spinner large"></span><h2>{busy}</h2><p>Your results will appear here.</p></div>
        {:else}
          <div class="empty-state"><span class="empty-icon" aria-hidden="true">▦</span><h2>{mode === 'sql' ? 'Ready for your query' : 'Choose a table'}</h2><p>{mode === 'sql' ? 'Write a SQL statement above and run it to see the results here.' : 'Choose a table in the sidebar to browse its first 100 rows.'}</p>{#if connection && !activeQuery.text}<span class="empty-shortcut">⌘ Enter runs your SQL</span>{/if}</div>
        {/if}
        {#if result?.truncated}<div class="limit-notice" role="status">Result truncated at the row or byte limit. Use a narrower SELECT or LIMIT to see a smaller result.</div>{/if}
      </section>
      {#if selectedCell}
        <aside class="detail-panel" aria-label="Cell details" style:width={`${Math.min(detailWidth, maxDetailWidth)}px`}><PanelDivider label="Resize cell details" orientation="vertical" value={Math.min(detailWidth, maxDetailWidth)} min={220} max={maxDetailWidth} reverse onresize={value => { detailWidth = value; }} /><div class="detail-heading"><span class="section-label">{editableCell ? 'Edit cell' : 'Cell details'}</span><button class="icon-button" aria-label="Close cell details" title="Close cell details (Esc)" onclick={closeCellDetails}>×</button></div><h2>{selectedCell.column}</h2><p class="detail-meta">{selectedCell.type} <span class="separator">·</span> Row {selectedRowNumber}</p><textarea bind:this={cellEditor} readonly={!editableCell} disabled={!!busy || (editableCell && selectedCell.value === null)} wrap="soft" aria-label="Selected cell value" class:null-value={selectedCell.value === null} value={selectedCell.value === null ? editableCell ? '' : 'NULL' : selectedCell.value} oninput={event => editCell(event.currentTarget.value)}></textarea>{#if editableCell}<label class="cell-null-toggle"><input type="checkbox" bind:this={cellNullToggle} disabled={!!busy} checked={selectedCell.value === null} onchange={event => editCell(event.currentTarget.checked ? null : '')} />Set SQL NULL</label>{/if}<p class="field-hint">{editableCell ? 'Changes are staged until you choose Save changes. Undo restores the loaded values.' : activeDeletedRows.includes(selectedCell.row) ? 'This row is marked for removal. Undo restores it.' : editReason}</p><p class="field-hint">{selectedCell.value === null ? 'This value is SQL NULL.' : selectedCell.value === '' ? 'This value is an empty string (0 characters).' : `${selectedCell.value.length.toLocaleString()} characters.`}</p></aside>
      {/if}
    </div>
    {/if}
    <footer class="statusbar" role="status"><span><span class="dot" class:connected={!!connection}></span>{busy || status}</span><span title={connection ? connectionEndpoint(connection) : undefined}>{connection ? connection.provider === 'sqlite' ? connectionEndpoint(connection) : `${connectionEndpoint(connection)} / ${connection.database}` : 'Not connected'}</span></footer>
  </main>
</div>

{#if rowMenu}
  <div id="row-context-menu" class="row-context-menu" role="menu" tabindex="-1" aria-label={rowMenu.rowIndices.length > 1 ? 'Selected rows actions' : `Row ${rowMenu.rowNumber} actions`} style:left={`${rowMenu.x}px`} style:top={`${rowMenu.y}px`} bind:this={rowMenuElement} onkeydown={rowMenuKeydown}>
    <button type="button" role="menuitem" bind:this={rowCopyButton} onclick={copyRow}>Copy row as CSV</button>
    {#if canRemoveRows && rowMenu.rowIndices.some(index => !activeDeletedRows.includes(index))}
      <button type="button" role="menuitem" onclick={() => removeRows(rowMenu!.rowIndices)}>{rowMenu.rowIndices.length > 1 ? 'Remove selected rows' : 'Remove row'}</button>
    {:else if !canRemoveRows}
      <p class="row-action-hint">{removalReason}</p>
    {/if}
  </div>
{/if}

{#if connection && editingSavedId}
  <dialog class="connection-dialog" bind:this={editDialog} aria-labelledby="connect-title" oncancel={event => { event.preventDefault(); if (!busy) cancelEdit(); }}>
    {@render connectionEditor()}
  </dialog>
{/if}

{#if pendingCloseQuery}
  <dialog class="query-close-dialog" bind:this={queryCloseDialog} aria-labelledby="close-query-title" aria-describedby="close-query-description" oncancel={event => { event.preventDefault(); cancelQueryClose(); }}>
    <h2 id="close-query-title">Close Query {queryPanels.indexOf(pendingCloseQuery) + 1}?</h2>
    <p id="close-query-description">This discards the SQL and results in this query tab.</p>
    <div class="query-close-actions">
      <button type="button" onclick={cancelQueryClose}>Keep query</button>
      <button type="button" class="primary" onclick={() => closeQuery(pendingCloseQuery!, true)}>Close query</button>
    </div>
  </dialog>
{/if}
