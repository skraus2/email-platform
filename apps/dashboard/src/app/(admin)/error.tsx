"use client";

/**
 * Shown inside the console layout when a page cannot load its data, e.g.
 * while the admin API is unreachable. Errors of the layout itself reach the
 * root error page.
 */
export default function AdminError({
  retry,
}: {
  error: Error & { digest?: string };
  retry: () => void;
}) {
  return (
    <>
      <title>Unavailable · Email Platform</title>
      <h1>This page could not be loaded</h1>
      <p role="alert">
        The data for this page is not available right now. Try again in a
        moment.
      </p>
      <button type="button" onClick={() => retry()}>
        Try again
      </button>
    </>
  );
}
