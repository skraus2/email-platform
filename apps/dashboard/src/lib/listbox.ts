/**
 * List logic for the dashboard's select and suggestion popups: which option
 * a key moves to, type-ahead and suggestion filtering. Options are referred
 * to by index; -1 means no active option. No imports: unit-tested with
 * node --test.
 */

/** Options PageUp/PageDown move by. */
export const PAGE_STEP = 10;

/**
 * The option a navigation key moves to from index, in a list of count
 * options, or undefined for other keys and empty lists. Moves stop at both
 * ends (no wrap). From no active option, ArrowDown goes to the first option
 * and ArrowUp to the last.
 */
export function moveIndex(
  key: string,
  index: number,
  count: number,
): number | undefined {
  if (count === 0) {
    return undefined;
  }
  const last = count - 1;
  const clamp = (i: number) => Math.min(Math.max(i, 0), last);
  switch (key) {
    case "ArrowDown":
      return index < 0 ? 0 : clamp(index + 1);
    case "ArrowUp":
      return index < 0 ? last : clamp(index - 1);
    case "Home":
      return 0;
    case "End":
      return last;
    case "PageDown":
      return clamp(index + PAGE_STEP);
    case "PageUp":
      return clamp(index - PAGE_STEP);
    default:
      return undefined;
  }
}

/**
 * The option type-ahead lands on: the first label that starts with query,
 * ignoring case, wrapping around. A query of several letters is still being
 * typed, so the search starts at current and stays there while it matches;
 * one letter, or one letter repeated ("bbb"), starts after current and so
 * cycles through the labels with that letter, as native selects do.
 * Undefined when nothing matches.
 */
export function typeahead(
  labels: readonly string[],
  query: string,
  current: number,
): number | undefined {
  const q = query.toLowerCase();
  if (q === "" || labels.length === 0) {
    return undefined;
  }
  const first = q.charAt(0);
  const repeated = Array.from(q).every((c) => c === first);
  const find = (prefix: string, start: number) =>
    labels
      .map((_, i) => (start + i) % labels.length)
      .find((i) => labels[i]?.toLowerCase().startsWith(prefix));
  const after = Math.max(current, -1) + 1;
  if (!repeated) {
    return find(q, Math.max(current, 0));
  }
  return find(q, after) ?? find(first, after);
}

/**
 * The suggestions containing text, ignoring case and surrounding spaces, in
 * their original order. Empty text keeps all of them.
 */
export function filterOptions(
  options: readonly string[],
  text: string,
): string[] {
  const t = text.trim().toLowerCase();
  return t === ""
    ? [...options]
    : options.filter((o) => o.toLowerCase().includes(t));
}
