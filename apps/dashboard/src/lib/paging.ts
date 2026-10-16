/** Cursor paging over admin API lists. No imports: unit-tested with node --test. */

export type Page<T> = { data: T[]; next_cursor: string | null };

/**
 * Loads pages (following next_cursor) until maxRows rows are collected or
 * the list ends. `more` tells whether rows beyond what was returned exist.
 * An empty page or a repeated cursor ends the loop as well, so a faulty
 * answer cannot make it run forever.
 */
export async function collectPages<T>(
  fetchPage: (cursor: string | null) => Promise<Page<T>>,
  maxRows: number,
): Promise<{ rows: T[]; more: boolean }> {
  const rows: T[] = [];
  const seen = new Set<string>();
  let cursor: string | null = null;
  for (;;) {
    const page: Page<T> = await fetchPage(cursor);
    rows.push(...page.data);
    cursor = page.next_cursor;
    if (
      cursor === null ||
      rows.length >= maxRows ||
      page.data.length === 0 ||
      seen.has(cursor)
    ) {
      break;
    }
    seen.add(cursor);
  }
  return {
    rows: rows.slice(0, maxRows),
    more: cursor !== null || rows.length > maxRows,
  };
}
