/**
 * How results being shown relate to one visit of the messages list. No
 * imports: unit-tested with node --test.
 */
export type ShownKind = "first" | "same" | "new";

/**
 * Classifies the results of `query` given the query the list was opened
 * with, the query of the results shown last in this visit (undefined when
 * none were marked yet) and whether the visit has navigated to another
 * query since it opened. Only the opening results of a visit that has not
 * navigated are first: the filters can be applied (and even undone) before
 * the opening results are revealed, so their effect never ran.
 */
export function shownKind(
  opened: string,
  last: string | undefined,
  query: string,
  navigated: boolean,
): ShownKind {
  if (last === undefined) {
    return query === opened && !navigated ? "first" : "new";
  }
  return last === query ? "same" : "new";
}
