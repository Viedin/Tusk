# Tusk agent instructions

## Product and interface

Tusk is a PostgreSQL desktop client for macOS. Design for connecting, browsing tables, and running SQL. Desktop is the target; mobile layouts are out of scope. Support the normal 1240×800 window and the 900×600 minimum. Use the frontend-design skill for UI changes.

- Keep a persistent app frame: saved connections and database navigation in the sidebar, the active task in the workspace, and connection status at the bottom. Connection setup belongs inside this frame.
- Switching between connection details and URL must change only the method-specific controls. Keep the heading, connection name, method switcher, and primary action anchored. Preserve entered values when switching methods.
- Provide draggable, keyboard-accessible panel dividers for sidebar width, saved connections versus tables, SQL editor versus results, and cell details. Clamp sizes so the workspace remains usable at 900×600. Keep the active connection pinned above the scrollable saved list.
- Keep workspace navigation and action positions stable between SQL and table views. Show Run query for SQL and Refresh rows for tables in the same primary-action slot. Use one context header per view; avoid repeated table names and redundant toolbar bands.
- Prioritize the active database and table tree in the connected sidebar. Keep saved connections reachable under Switch connection. Cell details should overlay the grid without changing column positions and close with Escape.
- Keep a visible, draggable horizontal scrollbar at the bottom of result grids, including short result sets. Constrain overflow to the grid so every column stays reachable.
- Connection colors identify saved environments through small accents on saved rows and the active connection. Names and endpoints must stay readable without relying on color. Allow editing without exposing saved secrets or disconnecting the current session.
- Saved connection rows should group a database icon, connection name, database, and host with an integrated delete control. Keep the connected state clear and preserve deletion confirmation.
- Keep Connect and cancellation actions visible in a fixed bottom action area. Let the form body scroll independently in smaller windows. Show connection errors above the action area so they remain visible without scrolling.
- Use compact, left-aligned headings and clear field labels. Avoid landing-page heroes, onboarding decorations, promotional copy, all-caps labels, and decorative cards or gradients.
- Preserve the quiet neutral palette: white `#FFFFFF`, sidebar `#F7F7F7`, borders `#E5E5E5`, ink `#1F1F1F`, muted text `#666666`. Use the existing dark primary action and understated selected states. Reserve error treatments for errors; connection color tags are identification accents.
- Use bundled Inter for interface text and JetBrains Mono for SQL and data values. Keep supporting labels in the interface font. Do not add remote fonts or dependencies for cosmetic changes.
- Reuse the existing elephant icon, controls, spacing, and CSS tokens. Borders should explain structure; avoid extra shadows and decoration.
- Write sentence-case, task-specific copy. Actions should name what happens, and errors should explain the problem and how to recover.
- Preserve keyboard navigation, visible focus, accessible names, disabled and busy states, cancellation, reduced-motion support, credential clearing, and connection validation.

## UI verification

- Run `npm run check`, `npm test`, and `npm run build` in `frontend` after UI changes.
- Review rendered screenshots of both connection methods at the default 1240×800 desktop size and the 900×600 minimum. Verify that the surrounding layout and primary action do not move when methods change.
- Check desktop layouts for horizontal overflow and reachable controls. Check errors, keyboard submission, retained input values, and the transition into and out of the database workspace when the change touches those flows.
- Use the real Wails backend or its loopback server test mode for workflow checks. Vite alone does not provide database services. Report any native or live-database behavior that was not checked.

## Browser preference

Prefer the Lightpanda MCP server for general web browsing: opening public pages, following links, reading content, extracting data, and interacting with pages. For a simple page read, use `markdown` with a `url`.

Use built-in web search for search-engine queries. Use a graphical browser for visual rendering, screenshots, or an existing signed-in session. If Lightpanda is unavailable or incompatible with a page, fall back to another available browser.
