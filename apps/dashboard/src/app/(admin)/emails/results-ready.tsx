"use client";

import { useEffect } from "react";

/** Set after the first results of this document were shown. */
let shownBefore = false;

/**
 * Announces new results in the page's persistent live region and, after an
 * in-page navigation (filters applied, next page) that removed the focused
 * element, moves focus to `focusId` so keyboard and screen-reader users keep
 * their place. A fresh page load keeps the browser's default focus.
 */
export function ResultsReady({
  message,
  focusId,
}: {
  message: string;
  focusId: string;
}) {
  useEffect(() => {
    const status = document.getElementById("results-status");
    if (status) {
      status.textContent = message;
    }
    const lost =
      !document.activeElement || document.activeElement === document.body;
    if (shownBefore && lost) {
      document.getElementById(focusId)?.focus();
    }
    shownBefore = true;
  }, [message, focusId]);
  return null;
}
