<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { Service } from '../bindings/tusk/internal/postgres';
  import { ConnectionsService } from '../bindings/tusk/internal/store';
  import SqlEditor from './SqlEditor.svelte';
  import PanelDivider from './PanelDivider.svelte';
  import { hasAffectedRows, resultStatus, sortedRowIndices } from './results';

  type SavedConnection = NonNullable<Awaited<ReturnType<typeof ConnectionsService.ListConnections>>>[number];
  type ConnectionInfo = Awaited<ReturnType<typeof Service.Connect>>;
  type TableInfo = NonNullable<Awaited<ReturnType<typeof Service.ListTables>>>[number];
  type CatalogColumn = NonNullable<Awaited<ReturnType<typeof Service.ListColumns>>>[number];
  type SortState = { columnIndex: number; columnName: string; descending: boolean };
  type RawResult = Awaited<ReturnType<typeof Service.RunQuery>>;
  type QueryResult = Omit<RawResult, 'columns' | 'rows'> & { columns: NonNullable<RawResult['columns']>; rows: (string | null)[][] };

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
  const visibleSavedConnections = $derived(savedConnections.filter(saved => `${saved.name} ${saved.database} ${saved.host} ${saved.user}`.toLowerCase().includes(savedSearch.trim().toLowerCase())));
  const activeConnectionColor = $derived(savedConnections.find(saved => saved.id === activeSavedId)?.color || '#666666');
  let connectionMode = $state<'fields' | 'url'>('fields');
  let connectionHost = $state('localhost');
  let connectionPort = $state(5432);
  let connectionDatabase = $state('postgres');
  let connectionUser = $state('');
  let connectionPassword = $state('');
  let connectionSSLMode = $state('prefer');
  const canConnect = $derived(connectionMode === 'url'
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
  let queryResult = $state<QueryResult | null>(null);
  let tableResult = $state<QueryResult | null>(null);
  let querySort = $state<SortState | null>(null);
  let tableSort = $state<SortState | null>(null);
  const currentSort = $derived(mode === 'sql' ? querySort : tableSort);
  const result = $derived(mode === 'sql' ? queryResult : tableResult);
  const displayedRows = $derived.by(() => {
    if (!result) return [];
    const indices = mode === 'sql' && querySort
      ? sortedRowIndices(result.rows, querySort.columnIndex, result.columns[querySort.columnIndex].dataType, querySort.descending)
      : result.rows.map((_, index) => index);
    return indices.map(sourceIndex => ({ sourceIndex, cells: result!.rows[sourceIndex] }));
  });
  let selectedCell = $state<{ column: string; type: string; value: string | null; row: number; columnIndex: number } | null>(null);
  const selectedRowNumber = $derived(displayedRows.findIndex(row => row.sourceIndex === selectedCell?.row) + 1);
  let busy = $state('');
  let cancelling = $state(false);
  const canCancel = $derived(!!busy && busy !== 'Saving connection…' && busy !== 'Saving changes…' && busy !== 'Deleting connection…');
  let error = $state('');
  let status = $state('Ready to connect');
  let sqlText = $state('');
  let editor = $state<{ getQuery(): string; focus(): void }>();

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
    busy = label;
    error = '';
    status = label;
    try { await action(); }
    catch (cause) { error = message(cause); status = 'Action failed'; }
    finally { busy = ''; cancelling = false; }
  }

  function clearResult(): void {
    if (mode === 'sql') queryResult = null;
    else tableResult = null;
    selectedCell = null;
  }

  onMount(() => { void loadSavedConnections(); });

  async function loadSavedConnections(): Promise<void> {
    savedLoading = true;
    storageError = '';
    try { savedConnections = await ConnectionsService.ListConnections() ?? []; }
    catch (cause) { storageError = message(cause); }
    finally { savedLoading = false; }
  }

  function connectionOptions() {
    return {
      host: connectionHost.trim(),
      port: connectionPort,
      database: connectionDatabase,
      user: connectionUser,
      password: connectionPassword,
      sslMode: connectionSSLMode,
    };
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
    queryResult = null;
    tableResult = null;
    querySort = null;
    tableSort = null;
    clearResult();
    availableSchemas = await Service.ListSchemas() ?? [];
    tables = await Service.ListTables() ?? [];
    await loadCatalogColumns();
    status = `Connected to ${connection.database}`;
    await tick();
    editor?.focus();
  }

  async function connect(): Promise<void> {
    if (!canConnect) return;
    await perform('Connecting…', async () => {
      const info = connectionMode === 'url'
        ? await Service.Connect(connectionURL.trim())
        : await Service.ConnectWithOptions(connectionOptions());
      await loadConnection(info);
    });
  }

  async function connectSaved(saved: SavedConnection): Promise<void> {
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
        mode: connectionMode,
        url: connectionMode === 'url' ? connectionURL.trim() : '',
        options: connectionMode === 'fields' ? connectionOptions() : { host: '', port: 0, database: '', user: '', password: '', sslMode: '' },
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
          || saved.database !== connection.database || saved.user !== connection.user;
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
      queryResult = null;
      tableResult = null;
      querySort = null;
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
    if (busy) return;
    mode = 'table';
    selectedTable = table;
    tableSort = sort;
    clearResult();
    await perform('Loading rows…', async () => {
      tableResult = normalizeResult(sort
        ? await Service.BrowseTableSorted(table.schema, table.name, sort.columnName, sort.descending)
        : await Service.BrowseTable(table.schema, table.name));
      status = `${tableResult.rowCount} rows loaded`;
    });
  }

  async function run(statement: string): Promise<void> {
    if (!connection || busy) return;
    if (!statement.trim()) { error = 'Enter a SQL statement, or select the statement you want to run.'; return; }
    mode = 'sql';
    querySort = null;
    clearResult();
    await perform('Running query…', async () => {
      queryResult = normalizeResult(await Service.RunQuery(statement));
      status = resultStatus(queryResult);
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
    else querySort = nextSort;
  }

  async function cancel(): Promise<void> {
    if (!canCancel || cancelling) return;
    cancelling = true;
    try { await Service.CancelQuery(); }
    catch (cause) { error = message(cause); cancelling = false; }
  }

  async function showSQL(): Promise<void> {
    mode = 'sql';
    selectedCell = null;
    await tick();
    editor?.focus();
  }

  function cellText(value: string | null): string { return value === null ? 'NULL' : value === '' ? '""' : value; }
</script>

<svelte:window bind:innerWidth={windowWidth} bind:innerHeight={windowHeight} onkeydown={event => { if (event.key === 'Escape') selectedCell = null; }} />

{#snippet resultMetrics()}
  {#if result}
    <span class="result-counts">{#if result.columns.length}{result.rowCount.toLocaleString()} rows<span class="separator">·</span>{/if}{result.durationMs} ms{#if hasAffectedRows(result.commandTag)}<span class="separator">·</span>{result.rowsAffected.toLocaleString()} affected{/if}</span>
  {/if}
{/snippet}

{#snippet connectionEditor()}
    <section class="connection-view" aria-labelledby="connect-title" aria-busy={!!busy}>
      <div class="connection-view-heading"><div><h1 id="connect-title">{editingSavedId ? 'Edit connection' : 'New connection'}</h1><p>{editingSavedId ? 'Update the saved name, color, and connection settings.' : 'Connect with individual details or a connection URL.'}</p></div></div>
        <form class="connection-form" onsubmit={event => { event.preventDefault(); void (editingSavedId ? saveEditedConnection() : connectionName.trim() ? saveAndConnect() : connect()); }} autocomplete="off">
          <div class="connection-scroll">
            <div class="connection-editor">
              <div class="connection-identity">
                <div class="connection-field">
                  <label for="connection-name">Connection name {#if !editingSavedId}<span class="optional-label">optional</span>{/if}</label>
                  <input id="connection-name" type="text" bind:value={connectionName} required={!!editingSavedId} maxlength="100" placeholder="e.g. Local development" autocomplete="off" disabled={!!busy} aria-describedby="connection-name-help" />
                </div>
                <p id="connection-name-help" class="field-hint">{editingSavedId ? 'Settings apply the next time you connect.' : 'Leave blank for a temporary connection.'}</p>
                <fieldset class="connection-colors" disabled={!!busy}><legend>Connection color</legend><div>{#each connectionColors as color}<button type="button" class="color-choice" class:chosen={connectionColor === color.value} style:--choice-color={color.value || '#999999'} aria-label={`${color.name} connection color`} aria-pressed={connectionColor === color.value} onclick={() => { connectionColor = color.value; }} title={color.name}><span aria-hidden="true">{color.value ? '' : '∕'}</span></button>{/each}</div></fieldset>
              </div>
              <div class="connection-modes" role="group" aria-label="Connection input format">
                <button type="button" class:active={connectionMode === 'fields'} aria-pressed={connectionMode === 'fields'} disabled={!!busy || !!editingSavedId} onclick={() => { connectionMode = 'fields'; error = ''; }}>Connection details</button>
                <button type="button" class:active={connectionMode === 'url'} aria-pressed={connectionMode === 'url'} disabled={!!busy || !!editingSavedId} onclick={() => { connectionMode = 'url'; error = ''; }}>Connection URL</button>
              </div>
              <div class="connection-method">
              {#if connectionMode === 'fields'}
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
          {#if error}<div class="connection-error" role="alert"><p>{error}</p></div>{/if}
          <div class="connect-actions">
            <div class="connect-actions-inner">
            <span class="connection-feedback" role="status">{#if busy}<span class="spinner"></span>{busy}{:else}{editingSavedId ? connection ? 'Your current session stays open.' : 'Save without connecting.' : 'Connect to browse tables and run SQL.'}{/if}</span>
            {#if editingSavedId}<button type="button" disabled={!!busy} onclick={cancelEdit}>Cancel</button>{/if}
            {#if canCancel}<button type="button" class="cancel-connect" disabled={cancelling} onclick={cancel}>{cancelling ? 'Cancelling…' : 'Cancel'}</button>{/if}
            <button class="primary connect-button" type="submit" disabled={!!busy || !canConnect || (!!editingSavedId && !connectionName.trim()) || (!!connectionName.trim() && savedLoading)}>
              {busy ? 'Please wait…' : editingSavedId ? 'Save changes' : connectionName.trim() ? 'Save & connect' : 'Connect'}
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
          <button class="saved-connect" disabled={!!busy || savedLoading} onclick={() => connectSaved(saved)} aria-label={`Connect to ${saved.name}`} title={`${saved.user}@${saved.host}:${saved.port}/${saved.database}`}>
            <span class="saved-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><ellipse cx="12" cy="5" rx="7" ry="3" /><path d="M5 5v14c0 1.7 3.1 3 7 3s7-1.3 7-3V5M5 12c0 1.7 3.1 3 7 3s7-1.3 7-3" /></svg></span>
            <span class="saved-info">
              <span class="saved-name-line"><span class="saved-name">{saved.name}</span>{#if activeSavedId === saved.id}<span class="saved-state">{activeSettingsChanged ? 'Reconnect' : 'Active'}</span>{/if}</span>
              <span class="saved-database">{saved.database}</span>
              <span class="saved-target">{saved.host}:{saved.port}</span>
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
    <div class="brand"><img class="brand-icon" src="/tusk-icon.png" alt="" aria-hidden="true" /><strong>Tusk</strong><span class="brand-subtitle">PostgreSQL</span></div>
    {#if connection}
    <section class="session-section" aria-label="Active connection" style:--connection-color={activeConnectionColor} style:height={savedSwitcherOpen ? `${visibleConnectionsHeight}px` : undefined}>
      <div class="session-heading"><span class="dot connected" style:background={activeConnectionColor}></span><strong title={connection.database}>{savedConnections.find(saved => saved.id === activeSavedId)?.name ?? connection.database}</strong><button class="quiet small" disabled={!!busy} onclick={disconnect}>Disconnect</button></div>
      <p class="session-database" title={`${connection.user}@${connection.host}:${connection.port} / ${connection.database}`}>{connection.database}<span class="session-endpoint">{connection.host}:{connection.port}</span></p>
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
              <button class:active={mode === 'table' && selectedTable?.schema === table.schema && selectedTable?.name === table.name} class="table-item" disabled={!!busy} onclick={() => browse(table)} title={`${table.schema}.${table.name} · ${table.type}`}><span class="table-symbol" aria-hidden="true">▦</span><span class="table-name">{table.name}</span>{#if table.type.toLowerCase().includes('view')}<span class="object-type">view</span>{/if}</button>
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
    <header class="connection-toolbar"><span>Connections</span><span class="toolbar-note">PostgreSQL</span></header>
    {@render connectionEditor()}
    {:else}
    <header class="workspace-toolbar">
      <nav class="workspace-views" aria-label="Workspace views">
        <button class:active={mode === 'sql'} aria-pressed={mode === 'sql'} onclick={showSQL}><span aria-hidden="true">⌘</span>SQL editor</button>
        <button class:active={mode === 'table'} aria-pressed={mode === 'table'} disabled={!selectedTable} onclick={() => { mode = 'table'; selectedCell = null; }}><span aria-hidden="true">▦</span>Table browser</button>
      </nav>
      <div class="workspace-actions" role="group" aria-label="Workspace actions">
        <button disabled={!canCancel || cancelling} onclick={cancel}>{cancelling ? 'Cancelling…' : 'Cancel'}</button>
        {#if mode === 'sql'}
          <button class="primary workspace-primary" disabled={!!busy || !sqlText.trim()} onclick={() => run(editor?.getQuery() ?? sqlText)} title="Run the selection or the entire editor (⌘ Enter)"><span aria-hidden="true">▶</span>Run query</button>
        {:else}
          <button class="primary workspace-primary" disabled={!!busy || !selectedTable} onclick={() => browse(selectedTable!, tableSort)}><span aria-hidden="true">↻</span>Refresh rows</button>
        {/if}
      </div>
    </header>
    <div class="workspace-context">
      <div class="context-heading">
        <h1 title={mode === 'sql' ? 'SQL query' : selectedTable?.name}>{mode === 'sql' ? 'SQL query' : selectedTable?.name}</h1>
        <p>{mode === 'sql' ? 'Runs with your account’s permissions, including writes.' : `${selectedTable?.schema} / ${selectedTable?.type}`}</p>
      </div>
      <div class="context-summary" role="status">
        {#if mode === 'sql'}<span class="keyboard-hint"><kbd>⌘</kbd><kbd>Enter</kbd> to run</span>
        {:else}{#if busy}<span class="running"><span class="spinner"></span>{busy}</span>{:else}{@render resultMetrics()}{/if}<span class="context-note">First 100 rows{#if tableSort} · Sorted {tableSort.descending ? 'descending' : 'ascending'}{/if}</span>{/if}
      </div>
    </div>
    <section class="query-section" class:hidden={mode !== 'sql'} aria-label="SQL editor" style:height={`${visibleEditorHeight}px`}>
      <div class="editor"><SqlEditor tables={tables} columns={catalogColumns} bind:this={editor} bind:value={sqlText} disabled={!connection || !!busy} onrun={run} /></div>
    </section>
    {#if mode === 'sql'}<PanelDivider label="Resize SQL editor" orientation="horizontal" value={visibleEditorHeight} min={150} max={maxEditorHeight} onresize={value => { editorHeight = value; }} />{/if}
    {#if error}<div class="error-banner" role="alert"><span aria-hidden="true">!</span><div>{error}</div><button aria-label="Dismiss error" onclick={() => { error = ''; }}>×</button></div>{/if}

    <div class="results-layout">
      <section class="results" aria-label="Query results" aria-busy={!!busy}>
        {#if mode === 'sql'}
          <div class="results-toolbar"><span class="section-label">Results</span>{@render resultMetrics()}{#if currentSort}<span class="sort-scope">Displayed rows · {currentSort.descending ? 'descending' : 'ascending'}</span>{/if}{#if busy}<span class="running"><span class="spinner"></span>{busy}</span>{/if}</div>
        {/if}
        {#if result && result.columns.length}
          <!-- svelte-ignore a11y_no_noninteractive_tabindex (The scroll region needs keyboard focus to reach off-screen columns.) -->
          <div class="grid-scroll" tabindex="0" role="region" aria-label="Scrollable result grid">
            <table class="data-grid">
              <thead>
                <tr>
                  <th class="row-number" aria-label="Row number">#</th>
                  {#each result.columns as column, columnIndex}
                    <th aria-sort={currentSort?.columnIndex === columnIndex ? currentSort.descending ? 'descending' : 'ascending' : 'none'}>
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
                  <tr>
                    <th class="row-number" scope="row">{rowIndex + 1}</th>
                    {#each row.cells as value, columnIndex}
                      <td><button class="cell" class:null-value={value === null} class:empty-value={value === ''} class:selected={selectedCell?.row === row.sourceIndex && selectedCell?.columnIndex === columnIndex} onclick={() => { selectedCell = { column: result!.columns[columnIndex].name, type: result!.columns[columnIndex].dataType, value, row: row.sourceIndex, columnIndex }; }} aria-label={`Row ${rowIndex + 1}, ${result.columns[columnIndex].name}: ${cellText(value)}`} title="Open cell details">{cellText(value)}</button></td>
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
          <div class="empty-state"><span class="empty-icon" aria-hidden="true">▦</span><h2>{mode === 'sql' ? 'Ready for your query' : 'Choose a table'}</h2><p>{mode === 'sql' ? 'Write a SQL statement above and run it to see the results here.' : 'Choose a table in the sidebar to browse its first 100 rows.'}</p>{#if connection && !sqlText}<span class="empty-shortcut">⌘ Enter runs your SQL</span>{/if}</div>
        {/if}
        {#if result?.truncated}<div class="limit-notice" role="status">Result truncated at the row or byte limit. Use a narrower SELECT or LIMIT to see a smaller result.</div>{/if}
      </section>
      {#if selectedCell}
        <aside class="detail-panel" aria-label="Cell details" style:width={`${Math.min(detailWidth, maxDetailWidth)}px`}><PanelDivider label="Resize cell details" orientation="vertical" value={Math.min(detailWidth, maxDetailWidth)} min={220} max={maxDetailWidth} reverse onresize={value => { detailWidth = value; }} /><div class="detail-heading"><span class="section-label">Cell details</span><button class="icon-button" aria-label="Close cell details" title="Close cell details (Esc)" onclick={() => { selectedCell = null; }}>×</button></div><h2>{selectedCell.column}</h2><p class="detail-meta">{selectedCell.type} <span class="separator">·</span> Row {selectedRowNumber}</p><textarea readonly wrap="soft" aria-label="Selected cell value" class:null-value={selectedCell.value === null} value={selectedCell.value === null ? 'NULL' : selectedCell.value}></textarea><p class="field-hint">{selectedCell.value === null ? 'This value is SQL NULL.' : selectedCell.value === '' ? 'This value is an empty string (0 characters).' : `${selectedCell.value.length.toLocaleString()} characters. Select text to copy.`}</p></aside>
      {/if}
    </div>
    {/if}
    <footer class="statusbar" role="status"><span><span class="dot" class:connected={!!connection}></span>{busy || status}</span><span>{connection ? `${connection.host}:${connection.port} / ${connection.database}` : 'Not connected'}</span></footer>
  </main>
</div>

{#if connection && editingSavedId}
  <dialog class="connection-dialog" bind:this={editDialog} aria-labelledby="connect-title" oncancel={event => { event.preventDefault(); if (!busy) cancelEdit(); }}>
    {@render connectionEditor()}
  </dialog>
{/if}
