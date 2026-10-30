import assert from "node:assert/strict";
import { test } from "node:test";
import { PAGE_STEP, filterOptions, moveIndex, typeahead } from "./listbox.ts";

test("moveIndex steps through the list and stops at both ends", () => {
  assert.equal(moveIndex("ArrowDown", 0, 3), 1);
  assert.equal(moveIndex("ArrowDown", 2, 3), 2);
  assert.equal(moveIndex("ArrowUp", 1, 3), 0);
  assert.equal(moveIndex("ArrowUp", 0, 3), 0);
  assert.equal(moveIndex("Home", 2, 3), 0);
  assert.equal(moveIndex("End", 0, 3), 2);
});

test("moveIndex enters the list from no active option", () => {
  assert.equal(moveIndex("ArrowDown", -1, 3), 0);
  assert.equal(moveIndex("ArrowUp", -1, 3), 2);
  assert.equal(moveIndex("Home", -1, 3), 0);
  assert.equal(moveIndex("End", -1, 3), 2);
});

test("moveIndex pages by PAGE_STEP and clamps", () => {
  assert.equal(moveIndex("PageDown", 0, 30), PAGE_STEP);
  assert.equal(moveIndex("PageDown", 25, 30), 29);
  assert.equal(moveIndex("PageUp", 15, 30), 15 - PAGE_STEP);
  assert.equal(moveIndex("PageUp", 3, 30), 0);
  assert.equal(moveIndex("PageDown", -1, 30), PAGE_STEP - 1);
});

test("moveIndex ignores other keys and empty lists", () => {
  assert.equal(moveIndex("a", 0, 3), undefined);
  assert.equal(moveIndex("Enter", 0, 3), undefined);
  assert.equal(moveIndex("ArrowDown", -1, 0), undefined);
  assert.equal(moveIndex("End", 0, 0), undefined);
});

const projects = ["All projects", "billing-app", "Billing-EU", "crm", "cms"];

test("typeahead finds the next label with the prefix, ignoring case", () => {
  assert.equal(typeahead(projects, "c", 0), 3);
  assert.equal(typeahead(projects, "CM", 0), 4);
  assert.equal(typeahead(projects, "billing-e", 0), 2);
});

test("typeahead searches after the current option and wraps around", () => {
  assert.equal(typeahead(projects, "b", 1), 2);
  assert.equal(typeahead(projects, "b", 2), 1);
  assert.equal(typeahead(projects, "a", 4), 0);
  // The current option itself is the last candidate.
  assert.equal(typeahead(projects, "all", 0), 0);
});

test("typeahead stays on the current label while it still matches", () => {
  const labels = ["All projects", "shop", "shop-eu"];
  // Keys one at a time, as a user types "shop": each step searches from
  // where the previous one landed.
  let current = 0;
  for (const query of ["s", "sh", "sho", "shop"]) {
    current = typeahead(labels, query, current) ?? current;
    assert.equal(current, 1, query);
  }
  assert.equal(typeahead(labels, "shop-", 1), 2);
  assert.equal(typeahead(labels, "shop", 2), 2);
});

test("typeahead cycles through labels on a repeated letter", () => {
  assert.equal(typeahead(projects, "cc", 3), 4);
  assert.equal(typeahead(projects, "ccc", 4), 3);
});

test("typeahead starts at the first option without an active one", () => {
  assert.equal(typeahead(projects, "b", -1), 1);
});

test("typeahead reports no match", () => {
  assert.equal(typeahead(projects, "x", 0), undefined);
  assert.equal(typeahead(projects, "cx", 0), undefined);
  assert.equal(typeahead(projects, "", 0), undefined);
  assert.equal(typeahead([], "a", -1), undefined);
});

const codes = ["provider_timeout", "provider_throttled", "suppressed"];

test("filterOptions keeps suggestions containing the text, in order", () => {
  assert.deepEqual(filterOptions(codes, "time"), ["provider_timeout"]);
  assert.deepEqual(filterOptions(codes, "T"), [
    "provider_timeout",
    "provider_throttled",
  ]);
  assert.deepEqual(filterOptions(codes, "  supp "), ["suppressed"]);
});

test("filterOptions keeps all on empty text and none without a match", () => {
  assert.deepEqual(filterOptions(codes, ""), codes);
  assert.deepEqual(filterOptions(codes, "   "), codes);
  assert.deepEqual(filterOptions(codes, "nope"), []);
});
