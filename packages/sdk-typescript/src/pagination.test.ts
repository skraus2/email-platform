import assert from "node:assert/strict";
import { test } from "node:test";

import { paginate, type Page } from "./pagination.ts";

function pages(...list: Page<number>[]) {
  const cursors: (string | undefined)[] = [];
  const fetchPage = (cursor?: string): Promise<Page<number>> => {
    cursors.push(cursor);
    const page = list[cursors.length - 1];
    if (page === undefined) {
      return Promise.reject(new Error("no more pages"));
    }
    return Promise.resolve(page);
  };
  return { cursors, fetchPage };
}

test("yields every item of every page, following next_cursor until null", async () => {
  const { cursors, fetchPage } = pages(
    { data: [1, 2], next_cursor: "c1" },
    { data: [], next_cursor: "c2" },
    { data: [3], next_cursor: null },
  );
  const items: number[] = [];
  for await (const item of paginate(fetchPage)) {
    items.push(item);
  }
  assert.deepEqual(items, [1, 2, 3]);
  assert.deepEqual(cursors, [undefined, "c1", "c2"]);
});

test("fetches lazily and stops when the consumer breaks", async () => {
  const { cursors, fetchPage } = pages(
    { data: [1, 2], next_cursor: "c1" },
    { data: [3], next_cursor: null },
  );
  const iterator = paginate(fetchPage);
  assert.equal(cursors.length, 0);
  for await (const item of iterator) {
    if (item === 1) {
      break;
    }
  }
  assert.deepEqual(cursors, [undefined]);
});

test("an error from a page call propagates", async () => {
  const { fetchPage } = pages({ data: [1], next_cursor: "c1" });
  const items: number[] = [];
  await assert.rejects(async () => {
    for await (const item of paginate(fetchPage)) {
      items.push(item);
    }
  }, /no more pages/);
  assert.deepEqual(items, [1]);
});

test("a cursor that does not advance throws instead of looping", async () => {
  const { fetchPage } = pages(
    { data: [1], next_cursor: "c1" },
    { data: [2], next_cursor: "c1" },
  );
  const items: number[] = [];
  await assert.rejects(async () => {
    for await (const item of paginate(fetchPage)) {
      items.push(item);
    }
  }, /next_cursor did not advance/);
  assert.deepEqual(items, [1, 2]);
});

test("a cursor seen before throws instead of cycling", async () => {
  const { cursors, fetchPage } = pages(
    { data: [1], next_cursor: "a" },
    { data: [2], next_cursor: "b" },
    { data: [3], next_cursor: "a" },
  );
  await assert.rejects(async () => {
    for await (const item of paginate(fetchPage)) {
      assert.ok(item <= 3);
    }
  }, /next_cursor did not advance/);
  assert.deepEqual(cursors, [undefined, "a", "b"]);
});

test("a page without a string or null next_cursor throws", async () => {
  for (const next of [undefined, 5, ""]) {
    const { cursors, fetchPage } = pages({
      data: [1],
      next_cursor: next as unknown as string,
    });
    await assert.rejects(async () => {
      for await (const item of paginate(fetchPage)) {
        assert.equal(item, 1);
      }
    }, /next_cursor must be a non-empty string or null/);
    assert.equal(cursors.length, 1);
  }
});
