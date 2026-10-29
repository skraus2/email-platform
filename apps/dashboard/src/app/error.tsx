"use client";

import Link from "next/link";

/**
 * Shown when a page cannot load, e.g. while the admin API is unreachable.
 * At the root it also catches errors of the (admin) layout.
 */
export default function ErrorPage({
  retry,
}: {
  error: Error & { digest?: string };
  retry: () => void;
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
        <button
          type="button"
          onClick={() => {
            retry();
          }}
        >
          Try again
        </button>
        <Link href="/login">Go to sign-in</Link>
      </div>
    </main>
  );
}
