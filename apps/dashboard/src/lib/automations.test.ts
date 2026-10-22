import assert from "node:assert/strict";
import { test } from "node:test";
import { describeReason, describeStep, formatWait } from "./automations.ts";

test("formats waits exactly", () => {
  assert.equal(formatWait(60), "1 minute");
  assert.equal(formatWait(3600), "1 hour");
  assert.equal(formatWait(90061), "1 day 1 hour 1 minute 1 second");
  assert.equal(formatWait(95400), "1 day 2 hours 30 minutes");
  assert.equal(formatWait(2592000), "30 days");
  assert.equal(formatWait(150), "2 minutes 30 seconds");
});

test("describes each step type", () => {
  assert.equal(describeStep({ type: "wait", seconds: 7200 }), "Wait 2 hours");
  assert.equal(
    describeStep({
      type: "condition",
      attribute: "plan",
      op: "eq",
      value: "pro",
    }),
    'Continue only if attribute "plan" is "pro"',
  );
  assert.equal(
    describeStep({ type: "condition", attribute: "vip", op: "exists" }),
    'Continue only if attribute "vip" is set',
  );
  assert.equal(
    describeStep({
      type: "condition",
      event: "order.completed",
      op: "not_occurred",
    }),
    'Continue only if event "order.completed" was not reported since the run started',
  );
  assert.equal(
    describeStep({
      type: "send",
      from: "news@example.test",
      template_id: "t",
      template_version: 3,
      unsubscribe_url: "https://x.test/{token}",
    }),
    "Send an email from news@example.test",
  );
});

test("describes end reasons", () => {
  assert.equal(describeReason("condition_not_met"), "a condition did not hold");
  assert.equal(describeReason("something_new"), "something_new");
});
