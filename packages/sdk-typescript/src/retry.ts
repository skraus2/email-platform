// How long to wait before the next attempt. Pure functions; `random`
// returns a number in [0, 1) like `Math.random`.

const maxRetryAfterSeconds = 60;
const retryAfterJitterMs = 1000;
const backoffBaseMs = 500;
const backoffCapMs = 8000;

/** Seconds from a `Retry-After` header; HTTP dates are not used. */
export function retryAfterSeconds(raw: string | null): number | undefined {
  if (raw === null || !/^[0-9]{1,9}$/.test(raw)) {
    return undefined;
  }
  return Number(raw);
}

/** Exponential backoff for attempt `n` (0-based), half of it jittered. */
export function backoffMs(n: number, random: () => number): number {
  const ceiling = Math.min(backoffCapMs, backoffBaseMs * 2 ** n);
  return Math.round(ceiling * (0.5 + 0.5 * random()));
}

/**
 * Wait before retry `n` (0-based): the server's `Retry-After`, clamped to
 * 1–60 s, plus jitter so clients that were refused together do not come
 * back together; without one, exponential backoff.
 */
export function waitMs(
  n: number,
  retryAfter: number | undefined,
  random: () => number,
): number {
  if (retryAfter === undefined) {
    return backoffMs(n, random);
  }
  const seconds = Math.min(maxRetryAfterSeconds, Math.max(1, retryAfter));
  return seconds * 1000 + Math.floor(random() * retryAfterJitterMs);
}
