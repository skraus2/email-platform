/** One page of a list operation, with wire names as the API sends them. */
export type Page<T> = { data: T[]; next_cursor: string | null };

/**
 * Iterates over every item of a list, fetching the next page only when the
 * previous one is used up. Each page is its own call with its own timeout
 * and retries.
 */
export async function* paginate<T>(
  fetchPage: (cursor?: string) => Promise<Page<T>>,
): AsyncGenerator<T, void, undefined> {
  let cursor: string | undefined;
  // A server that hands out an earlier cursor again would loop forever.
  const seen = new Set<string>();
  for (;;) {
    const page = await fetchPage(cursor);
    yield* page.data;
    const next: unknown = page.next_cursor;
    if (next === null) {
      return;
    }
    if (typeof next !== "string" || next === "") {
      throw new Error("next_cursor must be a non-empty string or null");
    }
    if (seen.has(next)) {
      throw new Error("next_cursor did not advance; stopping the iteration");
    }
    seen.add(next);
    cursor = next;
  }
}
