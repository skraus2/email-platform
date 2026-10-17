import assert from "node:assert/strict";
import { test } from "node:test";
import { shownKind } from "./results-visit.ts";

test("the results the list was opened with are the first of the visit", () => {
  assert.equal(
    shownKind("status=failed", undefined, "status=failed", false),
    "first",
  );
});

test("other results shown first come from a navigation inside the visit", () => {
  // The filters were applied before the opening results were revealed, so
  // their effect never ran.
  assert.equal(shownKind("", undefined, "status=failed", true), "new");
});

test("later results are the same or new compared with the last ones", () => {
  assert.equal(shownKind("", "status=failed", "status=failed", true), "same");
  assert.equal(shownKind("", "status=failed", "", true), "new");
  assert.equal(shownKind("", "", "recipient=a%40b.test", true), "new");
});

test("returning to the opening query before any results showed is new", () => {
  // Opened with "", filtered, then back to "" — all before a reveal.
  assert.equal(shownKind("", undefined, "", true), "new");
});
