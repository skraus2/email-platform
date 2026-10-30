import assert from "node:assert/strict";
import { test } from "node:test";
import {
  DAY_MS,
  MAX_DAY,
  MIN_DAY,
  addDays,
  addMonths,
  addYears,
  dayLabel,
  endOfWeek,
  formatDay,
  inRange,
  keyTarget,
  monthGrid,
  monthLabel,
  parseDay,
  sameMonth,
  startOfWeek,
  todayUTC,
  WEEKDAYS,
  type Day,
} from "./calendar.ts";

/** A day from a valid YYYY-MM-DD string; fails the test otherwise. */
function day(s: string): Day {
  const d = parseDay(s);
  if (d === undefined) {
    throw new Error(`not a date: ${s}`);
  }
  return d;
}

test("parses real YYYY-MM-DD dates with four-digit years", () => {
  assert.equal(parseDay("2026-10-30"), Date.UTC(2026, 9, 30));
  assert.equal(parseDay("2028-02-29"), Date.UTC(2028, 1, 29));
  assert.equal(parseDay("1000-01-01"), MIN_DAY);
  assert.equal(parseDay("9998-12-31"), MAX_DAY);
  for (const bad of [
    "",
    "2026-10-3",
    "2026-1-30",
    "26-10-30",
    "2026-10-30 ",
    "2026/10/30",
    "30.10.2026",
    "2026-13-01",
    "2026-00-10",
    "2026-02-30",
    "2027-02-29",
    "2100-02-29",
    "0999-12-31",
    "9999-01-01",
    "abc",
  ]) {
    assert.equal(parseDay(bad), undefined, bad);
  }
});

test("formats a day back to YYYY-MM-DD", () => {
  for (const s of ["2026-10-30", "2028-02-29", "1000-01-01", "9998-12-31"]) {
    assert.equal(formatDay(day(s)), s);
  }
});

test("today is the current UTC date, not the local one", () => {
  // 00:30 in Berlin on 31 October is still 30 October in UTC.
  assert.equal(
    formatDay(todayUTC(new Date("2026-10-30T23:30:00Z"))),
    "2026-10-30",
  );
  assert.equal(
    formatDay(todayUTC(new Date("2026-10-31T00:00:00Z"))),
    "2026-10-31",
  );
});

test("adding days crosses month and year ends and stays in range", () => {
  for (const [from, n, want] of [
    ["2026-10-31", 1, "2026-11-01"],
    ["2026-12-31", 1, "2027-01-01"],
    ["2028-03-01", -1, "2028-02-29"],
    ["2026-10-30", 7, "2026-11-06"],
    ["2026-10-30", -7, "2026-10-23"],
    ["1000-01-01", -1, "1000-01-01"],
    ["9998-12-31", 1, "9998-12-31"],
  ] as const) {
    assert.equal(formatDay(addDays(day(from), n)), want, `${from} ${n}`);
  }
});

test("adding months and years clamps the day of the month", () => {
  for (const [from, n, want] of [
    ["2026-10-30", 1, "2026-11-30"],
    ["2026-10-31", 1, "2026-11-30"],
    ["2028-01-31", 1, "2028-02-29"],
    ["2027-01-31", 1, "2027-02-28"],
    ["2026-03-31", -1, "2026-02-28"],
    ["2026-12-15", 1, "2027-01-15"],
    ["2026-01-15", -1, "2025-12-15"],
    ["2026-10-30", -22, "2024-12-30"],
    ["1000-01-15", -1, "1000-01-01"],
    ["9998-12-15", 1, "9998-12-31"],
  ] as const) {
    assert.equal(formatDay(addMonths(day(from), n)), want, `${from} ${n}`);
  }
  assert.equal(formatDay(addYears(day("2028-02-29"), 1)), "2029-02-28");
  assert.equal(formatDay(addYears(day("2028-02-29"), -4)), "2024-02-29");
  assert.equal(formatDay(addYears(day("2026-10-30"), -1)), "2025-10-30");
});

test("weeks run from Monday to Sunday", () => {
  // 2026-10-30 is a Friday.
  assert.equal(formatDay(startOfWeek(day("2026-10-30"))), "2026-10-26");
  assert.equal(formatDay(endOfWeek(day("2026-10-30"))), "2026-11-01");
  assert.equal(formatDay(startOfWeek(day("2026-10-26"))), "2026-10-26");
  assert.equal(formatDay(endOfWeek(day("2026-11-01"))), "2026-11-01");
  // Sunday belongs to the week before, not the one after.
  assert.equal(formatDay(startOfWeek(day("2026-11-01"))), "2026-10-26");
  assert.deepEqual(
    WEEKDAYS.map((w) => w.short),
    ["Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"],
  );
  assert.equal(WEEKDAYS[0].long, "Monday");
  assert.equal(WEEKDAYS[6].long, "Sunday");
});

test("the month grid has six Monday-first weeks around the month", () => {
  for (const [month, first, last] of [
    // October 2026 starts on a Thursday.
    ["2026-10-15", "2026-09-28", "2026-11-08"],
    // June 2026 starts on a Monday: no days of May before it.
    ["2026-06-01", "2026-06-01", "2026-07-12"],
    // February 2026 starts on a Sunday: six days of January before it.
    ["2026-02-10", "2026-01-26", "2026-03-08"],
    // February 2027: 28 days starting on a Monday.
    ["2027-02-28", "2027-02-01", "2027-03-14"],
    // February 2028: leap year.
    ["2028-02-01", "2028-01-31", "2028-03-12"],
  ] as const) {
    const start = day(first);
    assert.deepEqual(
      monthGrid(day(month)),
      Array.from({ length: 42 }, (_, i) => start + i * DAY_MS),
      month,
    );
    assert.equal(formatDay(start + 41 * DAY_MS), last, month);
  }
  const feb = monthGrid(day("2028-02-01")).filter((d) =>
    sameMonth(d, day("2028-02-15")),
  );
  assert.equal(feb.length, 29);
});

test("sameMonth compares year and month", () => {
  assert.equal(sameMonth(day("2026-10-01"), day("2026-10-31")), true);
  assert.equal(sameMonth(day("2026-10-31"), day("2026-11-01")), false);
  assert.equal(sameMonth(day("2025-10-15"), day("2026-10-15")), false);
});

test("a range needs both ends in order and includes them", () => {
  const from = day("2026-10-05");
  const to = day("2026-10-09");
  assert.equal(inRange(day("2026-10-05"), from, to), true);
  assert.equal(inRange(day("2026-10-07"), from, to), true);
  assert.equal(inRange(day("2026-10-09"), from, to), true);
  assert.equal(inRange(day("2026-10-04"), from, to), false);
  assert.equal(inRange(day("2026-10-10"), from, to), false);
  // One end missing or reversed: no range.
  assert.equal(inRange(day("2026-10-07"), from, undefined), false);
  assert.equal(inRange(day("2026-10-07"), undefined, to), false);
  assert.equal(inRange(day("2026-10-07"), to, from), false);
  assert.equal(inRange(from, from, from), true);
});

test("labels name days and months in English", () => {
  assert.equal(dayLabel(day("2026-10-30")), "Friday, 30 October 2026");
  assert.equal(dayLabel(day("2028-02-29")), "Tuesday, 29 February 2028");
  assert.equal(monthLabel(day("2026-10-30")), "October 2026");
  assert.equal(monthLabel(day("2027-01-01")), "January 2027");
});

test("grid keys move the focused day as in the APG date picker", () => {
  const fri = day("2026-10-30");
  for (const [key, shift, want] of [
    ["ArrowLeft", false, "2026-10-29"],
    ["ArrowRight", false, "2026-10-31"],
    ["ArrowUp", false, "2026-10-23"],
    ["ArrowDown", false, "2026-11-06"],
    ["Home", false, "2026-10-26"],
    ["End", false, "2026-11-01"],
    ["PageUp", false, "2026-09-30"],
    ["PageDown", false, "2026-11-30"],
    ["PageUp", true, "2025-10-30"],
    ["PageDown", true, "2027-10-30"],
  ] as const) {
    const got = keyTarget(key, shift, fri);
    assert.equal(
      got === undefined ? undefined : formatDay(got),
      want,
      `${key}${shift ? "+shift" : ""}`,
    );
  }
  for (const key of ["Enter", " ", "Tab", "a", "Escape"]) {
    assert.equal(keyTarget(key, false, fri), undefined, key);
  }
  assert.equal(
    formatDay(keyTarget("ArrowLeft", false, MIN_DAY) ?? 0),
    "1000-01-01",
  );
});
