"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
} from "react";
import { shownKind, type ShownKind } from "@/lib/results-visit";

/**
 * Records that the results of a query are shown and tells whether they are
 * the first of this visit, the same as last time, or new.
 */
type MarkShown = (query: string) => ShownKind;

const ShownContext = createContext<MarkShown | null>(null);

/**
 * One visit of the messages list. It stays mounted while only the query
 * changes (filters, paging) and is new after leaving the list, so going back
 * to it counts as a fresh visit. `query` is the raw query string of the
 * page; the one it mounts with is the visit's opening query.
 */
export function ResultsScope({
  query,
  children,
}: {
  query: string;
  children: React.ReactNode;
}) {
  const opened = useRef(query);
  const last = useRef<string | undefined>(undefined);
  const markShown = useCallback<MarkShown>((shown) => {
    const kind = shownKind(opened.current, last.current, shown);
    last.current = shown;
    return kind;
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
