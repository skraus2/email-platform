/**
 * How results being shown relate to one visit of the messages list. No
 * imports: unit-tested with node --test.
 */
export type ShownKind = "first" | "same" | "new";

/**
 * Classifies the results of `query` given the query the list was opened
 * with and the query of the results shown last in this visit (undefined
 * when none were marked yet). Results other than the opening ones are new
 * even when nothing was marked before: the filters can be applied before
 * the opening results are revealed, so their effect never ran.
 */
export function shownKind(
  opened: string,
  last: string | undefined,
  query: string,
): ShownKind {
  if (last === undefined) {
    return query === opened ? "first" : "new";
  }
  return last === query ? "same" : "new";
}
