import assert from "node:assert/strict";
import { test } from "node:test";
import { formatUTC, relativeTime } from "./format.ts";

test("formats timestamps in UTC to the second", () => {
  assert.equal(
    formatUTC("2026-10-16T09:05:07.123456Z"),
    "2026-10-16 09:05:07 UTC",
  );
  assert.equal(
    formatUTC("2026-10-16T11:05:07+02:00"),
    "2026-10-16 09:05:07 UTC",
  );
  assert.equal(formatUTC("garbage"), "garbage");
});

test("relative hints round down to the largest unit", () => {
  const now = new Date("2026-10-16T12:00:00Z");
  for (const [iso, want] of [
    ["2026-10-16T11:59:30Z", "just now"],
    ["2026-10-16T11:59:00Z", "1 minute ago"],
    ["2026-10-16T11:15:00Z", "45 minutes ago"],
    ["2026-10-16T10:00:00Z", "2 hours ago"],
    ["2026-10-15T12:00:00Z", "1 day ago"],
    ["2026-09-16T12:00:00Z", "30 days ago"],
    ["2026-10-16T12:30:00Z", "in 30 minutes"],
    ["2026-10-18T12:00:00Z", "in 2 days"],
  ]) {
    assert.equal(relativeTime(iso, now), want, iso);
  }
  assert.equal(relativeTime("garbage", now), "");
});
