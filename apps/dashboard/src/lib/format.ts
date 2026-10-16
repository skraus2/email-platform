/** Display helpers for timestamps. No imports: unit-tested with node --test. */

/** "2026-10-16 09:05:07 UTC"; unparsable input is returned unchanged. */
export function formatUTC(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) {
    return iso;
  }
  return new Date(t).toISOString().slice(0, 19).replace("T", " ") + " UTC";
}

const UNITS: [string, number][] = [
  ["day", 86_400_000],
  ["hour", 3_600_000],
  ["minute", 60_000],
];

/** "45 minutes ago", "in 2 days", "just now"; "" for unparsable input. */
export function relativeTime(iso: string, now: Date): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) {
    return "";
  }
  const diff = now.getTime() - t;
  const abs = Math.abs(diff);
  for (const [unit, ms] of UNITS) {
    const n = Math.floor(abs / ms);
    if (n >= 1) {
      const label = `${n} ${unit}${n === 1 ? "" : "s"}`;
      return diff >= 0 ? `${label} ago` : `in ${label}`;
    }
  }
  return "just now";
}
