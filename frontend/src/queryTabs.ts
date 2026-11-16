const storageKey = 'tusk.query-tabs.v1';

export type SavedQueryTabs = { texts: string[]; activeIndex: number };

export function loadQueryTabs(storage: Pick<Storage, 'getItem'>): SavedQueryTabs | null {
  const raw = storage.getItem(storageKey);
  if (raw === null) return null;
  const saved: unknown = JSON.parse(raw);
  if (typeof saved !== 'object' || saved === null || !('texts' in saved)
    || !Array.isArray(saved.texts) || !saved.texts.length || !saved.texts.every(text => typeof text === 'string')) {
    throw new Error('The saved SQL tabs have an invalid format.');
  }
  const activeIndex = 'activeIndex' in saved && typeof saved.activeIndex === 'number'
    && Number.isInteger(saved.activeIndex) && saved.activeIndex >= 0 && saved.activeIndex < saved.texts.length
    ? saved.activeIndex : 0;
  return { texts: saved.texts, activeIndex };
}

export function saveQueryTabs(storage: Pick<Storage, 'setItem'>, texts: readonly string[], activeIndex: number): void {
  storage.setItem(storageKey, JSON.stringify({ texts, activeIndex }));
}
