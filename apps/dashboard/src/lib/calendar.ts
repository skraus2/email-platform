/**
 * Date math for the date fields, all in UTC: the dashboard filters by UTC
 * days. A day is the millisecond timestamp of its 00:00 UTC, so days compare
 * and sort as numbers. Names are English and weeks start on Monday,
 * whatever the browser's locale. No imports: unit-tested with node --test.
 */

/** Milliseconds of 00:00 UTC of a day. */
export type Day = number;

export const DAY_MS = 24 * 60 * 60 * 1000;

/** First and last day a date field accepts: four-digit years, with room for
 *  the day after the last one (an inclusive "to" date + 1). */
export const MIN_DAY: Day = Date.UTC(1000, 0, 1);
export const MAX_DAY: Day = Date.UTC(9998, 11, 31);

/** Monday first. */
export const WEEKDAYS = [
  { short: "Mo", long: "Monday" },
  { short: "Tu", long: "Tuesday" },
  { short: "We", long: "Wednesday" },
  { short: "Th", long: "Thursday" },
  { short: "Fr", long: "Friday" },
  { short: "Sa", long: "Saturday" },
  { short: "Su", long: "Sunday" },
] as const;

const MONTHS = [
  "January",
  "February",
  "March",
  "April",
  "May",
  "June",
  "July",
  "August",
  "September",
  "October",
  "November",
  "December",
] as const;

/** The day of a YYYY-MM-DD date between MIN_DAY and MAX_DAY, undefined if invalid. */
export function parseDay(s: string): Day | undefined {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s);
  if (!m) {
    return undefined;
  }
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
  const t = Date.UTC(y, mo - 1, d);
  const back = new Date(t);
  if (
    back.getUTCFullYear() !== y ||
    back.getUTCMonth() !== mo - 1 ||
    back.getUTCDate() !== d ||
    t < MIN_DAY ||
    t > MAX_DAY
  ) {
    return undefined;
  }
  return t;
}

/** YYYY-MM-DD of a day. */
export function formatDay(d: Day): string {
  return new Date(d).toISOString().slice(0, 10);
}

/** The current UTC date. */
export function todayUTC(now: Date): Day {
  return Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
}

function clamp(d: Day): Day {
  return Math.min(Math.max(d, MIN_DAY), MAX_DAY);
}

export function addDays(d: Day, n: number): Day {
  return clamp(d + n * DAY_MS);
}

/** The same day of the month n months later, or the month's last day if it
 *  is shorter (31 January + 1 month = 28/29 February). */
export function addMonths(d: Day, n: number): Day {
  const date = new Date(d);
  const months = date.getUTCMonth() + n;
  const y = date.getUTCFullYear() + Math.floor(months / 12);
  const m = ((months % 12) + 12) % 12;
  const daysInMonth = new Date(Date.UTC(y, m + 1, 0)).getUTCDate();
  return clamp(Date.UTC(y, m, Math.min(date.getUTCDate(), daysInMonth)));
}

export function addYears(d: Day, n: number): Day {
  return addMonths(d, 12 * n);
}

/** 0 for Monday … 6 for Sunday. */
function weekday(d: Day): number {
  return (new Date(d).getUTCDay() + 6) % 7;
}

/** The Monday of the day's week. */
export function startOfWeek(d: Day): Day {
  return addDays(d, -weekday(d));
}

/** The Sunday of the day's week. */
export function endOfWeek(d: Day): Day {
  return addDays(d, 6 - weekday(d));
}

export function sameMonth(a: Day, b: Day): boolean {
  const x = new Date(a);
  const y = new Date(b);
  return (
    x.getUTCFullYear() === y.getUTCFullYear() &&
    x.getUTCMonth() === y.getUTCMonth()
  );
}

/** The 42 days (six Monday-first weeks) shown for the month of a day: the
 *  week of its first day and the five after it. Not clamped, so the first
 *  and last months may show days outside MIN_DAY…MAX_DAY. */
export function monthGrid(d: Day): Day[] {
  const date = new Date(d);
  const first = Date.UTC(date.getUTCFullYear(), date.getUTCMonth(), 1);
  const start = first - weekday(first) * DAY_MS;
  return Array.from({ length: 42 }, (_, i) => start + i * DAY_MS);
}

/** Whether a day lies in from…to (both included); false unless both ends
 *  are set and in order. */
export function inRange(d: Day, from?: Day, to?: Day): boolean {
  return (
    from !== undefined && to !== undefined && from <= to && from <= d && d <= to
  );
}

/** "Friday, 30 October 2026". */
export function dayLabel(d: Day): string {
  const date = new Date(d);
  return `${WEEKDAYS[weekday(d)]?.long ?? ""}, ${String(date.getUTCDate())} ${monthLabel(d)}`;
}

/** "October 2026". */
export function monthLabel(d: Day): string {
  const date = new Date(d);
  return `${MONTHS[date.getUTCMonth()] ?? ""} ${String(date.getUTCFullYear())}`;
}
