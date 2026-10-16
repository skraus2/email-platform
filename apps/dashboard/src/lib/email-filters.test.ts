import assert from "node:assert/strict";
import { test } from "node:test";
import {
  apiQuery,
  emailsHref,
  hasFilters,
  isFailuresView,
  parseEmailFilters,
} from "./email-filters.ts";

const PROJECT = "0199f0a2-7c1e-7d3a-9b1e-2f5c8a9d0e11";
const TEMPLATE = "0199f0a2-7c1e-7d3a-9b1e-2f5c8a9d0e22";

test("no parameters means no filters", () => {
  const { filters, errors } = parseEmailFilters({});
  assert.deepEqual(errors, []);
  assert.deepEqual(filters, { status: [] });
  assert.equal(hasFilters(filters), false);
  assert.equal(apiQuery(filters).toString(), "limit=50");
  assert.equal(emailsHref(filters), "/emails");
});

test("empty and blank form fields are ignored", () => {
  const { filters, errors } = parseEmailFilters({
    project_id: "",
    recipient: "  ",
    from: "",
    to: "",
    error_code: "",
    q: " ",
    template_id: "",
    cursor: "",
  });
  assert.deepEqual(errors, []);
  assert.deepEqual(filters, { status: [] });
});

test("valid filters map to the admin API query", () => {
  const { filters, errors } = parseEmailFilters({
    project_id: PROJECT.toUpperCase(),
    status: ["failed", "bounced", "failed"],
    recipient: " Ann@Example.test ",
    from: "2026-10-01",
    to: "2026-10-15",
    error_code: "provider_rejected",
    q: " abc ",
    template_id: TEMPLATE,
    cursor: "AZnwonwefTqbHi9cip0OEQ",
  });
  assert.deepEqual(errors, []);
  assert.equal(hasFilters(filters), true);
  assert.equal(
    apiQuery(filters).toString(),
    new URLSearchParams([
      ["project_id", PROJECT],
      ["status", "failed"],
      ["status", "bounced"],
      ["recipient", "Ann@Example.test"],
      ["created_after", "2026-10-01T00:00:00Z"],
      ["created_before", "2026-10-16T00:00:00Z"],
      ["error_code", "provider_rejected"],
      ["q", "abc"],
      ["template_id", TEMPLATE],
      ["limit", "50"],
      ["cursor", "AZnwonwefTqbHi9cip0OEQ"],
    ]).toString(),
  );
});

test("the to date is inclusive across month and year ends", () => {
  for (const [to, before] of [
    ["2026-10-31", "2026-11-01T00:00:00Z"],
    ["2026-12-31", "2027-01-01T00:00:00Z"],
    ["2028-02-28", "2028-02-29T00:00:00Z"],
  ]) {
    const { filters } = parseEmailFilters({ to });
    assert.equal(apiQuery(filters).get("created_before"), before, to);
  }
  const { filters, errors } = parseEmailFilters({
    from: "2026-10-05",
    to: "2026-10-05",
  });
  assert.deepEqual(errors, []);
  assert.equal(apiQuery(filters).get("created_after"), "2026-10-05T00:00:00Z");
  assert.equal(apiQuery(filters).get("created_before"), "2026-10-06T00:00:00Z");
});

test("invalid values are reported per field without calling the API", () => {
  const cases: [Record<string, string | string[]>, string][] = [
    [{ project_id: "nope" }, "project_id"],
    [{ project_id: `${PROJECT}x` }, "project_id"],
    [{ status: "nope" }, "status"],
    [{ status: ["failed", "Failed"] }, "status"],
    [{ recipient: "no-at-sign" }, "recipient"],
    [{ recipient: "a@b.test, c@d.test" }, "recipient"],
    [{ recipient: `${"a".repeat(250)}@b.test` }, "recipient"],
    [{ from: "2026-02-30" }, "from"],
    [{ from: "16.10.2026" }, "from"],
    [{ to: "2026-13-01" }, "to"],
    [{ to: "9999-12-31" }, "to"],
    [{ from: "0099-01-01" }, "from"],
    [{ from: "2026-10-02", to: "2026-10-01" }, "to"],
    [{ error_code: "Bad-Code" }, "error_code"],
    [{ error_code: "a".repeat(41) }, "error_code"],
    [{ q: "x".repeat(513) }, "q"],
    [{ template_id: "1" }, "template_id"],
    [{ cursor: "c".repeat(201) }, "cursor"],
    [{ recipient: ["a@b.test", "c@d.test"] }, "recipient"],
    [{ q: ["a", "b"] }, "q"],
  ];
  for (const [params, field] of cases) {
    const { errors } = parseEmailFilters(params);
    assert.deepEqual(
      errors.map((e) => e.field),
      [field],
      JSON.stringify(params),
    );
    for (const e of errors) {
      for (const v of Object.values(params).flat()) {
        assert.ok(
          v.length < 3 || !e.message.includes(v),
          `message echoes value: ${e.message}`,
        );
      }
    }
  }
});

test("q counts characters, not UTF-16 units", () => {
  assert.deepEqual(parseEmailFilters({ q: "😀".repeat(512) }).errors, []);
});

test("links keep filters, add the cursor only when asked", () => {
  const { filters } = parseEmailFilters({
    status: ["failed", "bounced"],
    q: "a&b=c",
    cursor: "CUR",
  });
  assert.equal(
    emailsHref(filters),
    "/emails?status=failed&status=bounced&q=a%26b%3Dc",
  );
  assert.equal(
    emailsHref(filters, "NEXT"),
    "/emails?status=failed&status=bounced&q=a%26b%3Dc&cursor=NEXT",
  );
  const dated = parseEmailFilters({ from: "2026-10-01", to: "2026-10-02" });
  assert.equal(
    emailsHref(dated.filters),
    "/emails?from=2026-10-01&to=2026-10-02",
  );
});

test("the failures view is exactly status=failed", () => {
  const view = (p: Record<string, string | string[]>) =>
    isFailuresView(parseEmailFilters(p).filters);
  assert.equal(view({ status: "failed" }), true);
  assert.equal(view({ status: "failed", cursor: "CUR" }), true);
  assert.equal(view({}), false);
  assert.equal(view({ status: ["failed", "bounced"] }), false);
  assert.equal(view({ status: "failed", q: "x" }), false);
});
