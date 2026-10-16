import assert from "node:assert/strict";
import { test } from "node:test";
import { collectPages } from "./paging.ts";

/** A fake list API over `total` rows with pages of `size`. */
function pages(total: number, size = 100) {
  const calls: (string | null)[] = [];
  const fetchPage = async (cursor: string | null) => {
    calls.push(cursor);
    const start = cursor ? Number(cursor) : 0;
    const data = Array.from(
      { length: Math.max(0, Math.min(size, total - start)) },
      (_, i) => start + i,
    );
    const end = start + data.length;
    return { data, next_cursor: end < total ? String(end) : null };
  };
  return { fetchPage, calls };
}

test("a short single page has no more rows", async () => {
  const api = pages(7);
  const got = await collectPages(api.fetchPage, 500);
  assert.equal(got.rows.length, 7);
  assert.equal(got.more, false);
  assert.deepEqual(api.calls, [null]);
});

test("exactly the cap with no cursor left has no more rows", async () => {
  const api = pages(500);
  const got = await collectPages(api.fetchPage, 500);
  assert.equal(got.rows.length, 500);
  assert.equal(got.more, false);
  assert.deepEqual(api.calls, [null, "100", "200", "300", "400"]);
});

test("the cap with a cursor left reports more rows", async () => {
  const api = pages(501);
  const got = await collectPages(api.fetchPage, 500);
  assert.equal(got.rows.length, 500);
  assert.equal(got.more, true);
  assert.equal(api.calls.length, 5);
});

test("a page overshooting the cap is cut and reports more rows", async () => {
  const api = pages(250, 100);
  const got = await collectPages(api.fetchPage, 150);
  assert.deepEqual(
    got.rows,
    Array.from({ length: 150 }, (_, i) => i),
  );
  assert.equal(got.more, true);
});

test("an empty page or a repeated cursor ends the loop", async () => {
  let calls = 0;
  const empty = await collectPages(async () => {
    calls++;
    return { data: [], next_cursor: "again" };
  }, 500);
  assert.deepEqual(empty, { rows: [], more: true });
  assert.equal(calls, 1);

  calls = 0;
  const stuck = await collectPages(async () => {
    calls++;
    return { data: [calls], next_cursor: "same" };
  }, 500);
  assert.deepEqual(stuck, { rows: [1, 2], more: true });
  assert.equal(calls, 2);
});
