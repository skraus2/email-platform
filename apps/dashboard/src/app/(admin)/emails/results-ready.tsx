"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
} from "react";

/**
 * Records that the results of a query are shown and tells whether they are
 * the first of this visit, the same as last time, or new.
 */
type MarkShown = (query: string) => "first" | "same" | "new";

const ShownContext = createContext<MarkShown | null>(null);

/**
 * One visit of the messages list. It stays mounted while only the query
 * changes (filters, paging) and is new after leaving the list, so going back
 * to it counts as a fresh visit.
 */
export function ResultsScope({ children }: { children: React.ReactNode }) {
  const last = useRef<string | undefined>(undefined);
  const markShown = useCallback<MarkShown>((query) => {
    const before = last.current;
    last.current = query;
    return before === undefined ? "first" : before === query ? "same" : "new";
  }, []);
  return (
    <ShownContext.Provider value={markShown}>{children}</ShownContext.Provider>
  );
}

/**
 * Marks the results of `query` as shown. On the first results of a visit it
 * does nothing (the browser keeps its focus and scroll position). After an
 * in-page navigation to another query it announces `message` in the page's
 * persistent live region and, when that navigation removed the focused
 * element, moves focus to `focusId` so keyboard and screen-reader users keep
 * their place.
 */
export function ResultsReady({
  query,
  message,
  focusId,
}: {
  /** The raw query string of the page, cursor and invalid values included. */
  query: string;
  /** Announced politely; omitted where the focus target already says it. */
  message?: string;
  focusId: string;
}) {
  const markShown = useContext(ShownContext);
  useEffect(() => {
    if (markShown?.(query) !== "new") {
      return;
    }
    const status = document.getElementById("results-status");
    if (status) {
      // Cleared first, so an identical message is announced again and no
      // stale count stays behind when there is nothing to announce.
      status.textContent = "";
      if (message) {
        requestAnimationFrame(() => {
          status.textContent = message;
        });
      }
    }
    const lost =
      !document.activeElement || document.activeElement === document.body;
    if (lost) {
      document.getElementById(focusId)?.focus();
    }
  }, [markShown, query, message, focusId]);
  return null;
}
