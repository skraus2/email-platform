import assert from "node:assert/strict";
import { test } from "node:test";

import { backoffMs, retryAfterSeconds, waitMs } from "./retry.ts";

test("Retry-After: only a plain non-negative integer is usable", () => {
  assert.equal(retryAfterSeconds("2"), 2);
  assert.equal(retryAfterSeconds("0"), 0);
  assert.equal(retryAfterSeconds("120"), 120);
  for (const raw of [
    null,
    "",
    " 2",
    "2 ",
    "+2",
    "-1",
    "1.5",
    "2e1",
    "abc",
    "Wed, 21 Oct 2026 07:28:00 GMT",
    "99999999999999999999",
  ]) {
    assert.equal(retryAfterSeconds(raw), undefined, String(raw));
  }
});

test("a Retry-After wait is clamped to 1–60 s plus up to 1 s of jitter", () => {
  assert.equal(
    waitMs(0, 2, () => 0),
    2000,
  );
  assert.equal(
    waitMs(0, 2, () => 0.999),
    2999,
  );
  assert.equal(
    waitMs(3, 0, () => 0),
    1000,
  );
  assert.equal(
    waitMs(0, 120, () => 0),
    60_000,
  );
  assert.equal(
    waitMs(0, 120, () => 0.5),
    60_500,
  );
});

test("without Retry-After: exponential backoff, capped at 8 s, half of it jittered", () => {
  assert.equal(
    backoffMs(0, () => 0),
    250,
  );
  assert.equal(
    backoffMs(0, () => 1),
    500,
  );
  assert.equal(
    backoffMs(1, () => 0),
    500,
  );
  assert.equal(
    backoffMs(2, () => 1),
    2000,
  );
  assert.equal(
    backoffMs(4, () => 1),
    8000,
  );
  assert.equal(
    backoffMs(10, () => 1),
    8000,
  );
  assert.equal(
    backoffMs(10, () => 0),
    4000,
  );
  assert.equal(
    waitMs(1, undefined, () => 1),
    1000,
  );
});
