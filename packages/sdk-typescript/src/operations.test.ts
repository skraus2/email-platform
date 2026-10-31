import assert from "node:assert/strict";
import { test } from "node:test";

import { OPERATIONS, isRetryable, type OperationId } from "./operations.ts";

test("the table has one entry per /v1 operation", () => {
  assert.equal(Object.keys(OPERATIONS).length, 37);
});

test("only reads and the two operations that honour an idempotency key are retried", () => {
  const retryable = (Object.keys(OPERATIONS) as OperationId[])
    .filter((id) => isRetryable(id))
    .sort();
  assert.deepEqual(retryable, [
    "createContactEvent",
    "createEmail",
    "getAudience",
    "getAutomation",
    "getAutomationRun",
    "getBroadcast",
    "getContact",
    "getEmail",
    "getTemplate",
    "getTemplateVersion",
    "getUsage",
    "listAudienceContacts",
    "listAudiences",
    "listAutomationRuns",
    "listAutomations",
    "listBroadcasts",
    "listContacts",
    "listEmailEvents",
    "listEmails",
  ]);
});

test("only createEmail and createContactEvent take an idempotency key", () => {
  const keyed = (Object.keys(OPERATIONS) as OperationId[])
    .filter((id) => "idempotencyKey" in OPERATIONS[id])
    .sort();
  assert.deepEqual(keyed, ["createContactEvent", "createEmail"]);
});

test("every path starts with /v1/", () => {
  for (const [id, op] of Object.entries(OPERATIONS)) {
    assert.match(op.path, /^\/v1\//, id);
  }
});
