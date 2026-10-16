"use client";

import Link from "next/link";

/**
 * Shown when a page cannot load, e.g. while the admin API is unreachable.
 * At the root it also catches errors of the (admin) layout.
 */
export default function ErrorPage({
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  return (
    <main className="login">
      <title>Unavailable · Email Platform</title>
      <h1>This page could not be loaded</h1>
      <p>
        The operator console cannot reach its data right now. Try again in a
        moment.
      </p>
      <div className="actions">
        <button type="button" onClick={reset}>
          Try again
        </button>
        <Link href="/login">Go to sign-in</Link>
      </div>
    </main>
  );
}
