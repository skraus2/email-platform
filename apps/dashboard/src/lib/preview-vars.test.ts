import assert from "node:assert/strict";
import { test } from "node:test";
import {
  apiFieldError,
  buildPreviewVariables,
  previewFailure,
  fieldName,
  isVersion,
  variableEntries,
  variableNames,
  type VariableSchema,
} from "./preview-vars.ts";

const SCHEMA = {
  name: { type: "string", required: true, max_length: 5 },
  note: { type: "string", required: false, max_length: 1000 },
  amount: { type: "number", required: true },
  discount: { type: "number", required: false },
  vip: { type: "boolean", required: true },
  beta: { type: "boolean", required: false },
} satisfies VariableSchema;

function form(values: Record<string, string>) {
  return (field: string) => values[field] ?? null;
}

test("builds typed variables from form fields", () => {
  const out = buildPreviewVariables(
    SCHEMA,
    form({
      "v.name": "Ann",
      "v.note": "",
      "v.amount": " 12.50 ",
      "v.discount": "-3e2",
      "v.vip": "true",
      "v.beta": "false",
    }),
  );
  assert.deepEqual(out.errors, []);
  assert.deepEqual(out.variables, {
    name: "Ann",
    note: "",
    amount: 12.5,
    discount: -300,
    vip: true,
    beta: false,
  });
});

test("strings are always sent; empty numbers and unset booleans are omitted", () => {
  const out = buildPreviewVariables(
    { note: SCHEMA.note, discount: SCHEMA.discount, beta: SCHEMA.beta },
    form({}),
  );
  assert.deepEqual(out.errors, []);
  assert.deepEqual(out.variables, { note: "" });
});

test("required number and boolean need a value", () => {
  const out = buildPreviewVariables(
    { amount: SCHEMA.amount, vip: SCHEMA.vip },
    form({ "v.amount": "  ", "v.vip": "" }),
  );
  assert.deepEqual(out.variables, {});
  assert.deepEqual(
    out.errors.map((e) => e.name),
    ["amount", "vip"],
  );
});

test("numbers must be finite decimal literals", () => {
  for (const bad of [
    "1e400",
    "NaN",
    "Infinity",
    "0x10",
    "1,5",
    "12abc",
    "--1",
    "1 2",
    ".",
    "e5",
  ]) {
    const out = buildPreviewVariables(
      { amount: SCHEMA.amount },
      form({ "v.amount": bad }),
    );
    assert.equal(out.errors.length, 1, bad);
    assert.equal(out.errors[0]?.name, "amount");
    assert.deepEqual(out.variables, {}, bad);
  }
  for (const [good, want] of [
    ["0", 0],
    ["-0.5", -0.5],
    [".5", 0.5],
    ["5.", 5],
    ["+7", 7],
    ["1E3", 1000],
  ] as const) {
    const out = buildPreviewVariables(
      { amount: SCHEMA.amount },
      form({ "v.amount": good }),
    );
    assert.deepEqual(out.errors, [], good);
    assert.equal(out.variables["amount"], want, good);
  }
});

test("booleans accept only true and false", () => {
  const out = buildPreviewVariables(
    { beta: SCHEMA.beta },
    form({ "v.beta": "yes" }),
  );
  assert.deepEqual(out.variables, {});
  assert.equal(out.errors[0]?.name, "beta");
});

test("strings longer than max_length (in characters) are refused", () => {
  const ok = buildPreviewVariables(
    { name: SCHEMA.name },
    form({ "v.name": "😀😀😀😀😀" }),
  );
  assert.deepEqual(ok.errors, []);
  const long = buildPreviewVariables(
    { name: SCHEMA.name },
    form({ "v.name": "abcdef" }),
  );
  assert.equal(long.errors[0]?.name, "name");
  assert.deepEqual(long.variables, {});
});

test("fields of undeclared names are ignored", () => {
  const out = buildPreviewVariables(
    { note: SCHEMA.note },
    form({ "v.note": "x", "v.other": "y", other: "z" }),
  );
  assert.deepEqual(out.variables, { note: "x" });
});

test("error messages never contain the value", () => {
  const secret = "Zq9secret";
  const out = buildPreviewVariables(SCHEMA, (field) =>
    field === "v.name" ? `${secret}${secret}` : `${secret}`,
  );
  assert.ok(out.errors.length > 0);
  for (const e of out.errors) {
    assert.ok(!e.message.includes(secret), e.message);
  }
});

test("variable names are sorted; field names are prefixed", () => {
  assert.deepEqual(variableNames(SCHEMA), [
    "amount",
    "beta",
    "discount",
    "name",
    "note",
    "vip",
  ]);
  assert.deepEqual(
    variableEntries(SCHEMA).map(([name, spec]) => [name, spec.type]),
    [
      ["amount", "number"],
      ["beta", "boolean"],
      ["discount", "number"],
      ["name", "string"],
      ["note", "string"],
      ["vip", "boolean"],
    ],
  );
  assert.equal(fieldName("name"), "v.name");
});

test("version numbers are positive integers without leading zeros", () => {
  for (const v of ["1", "9", "10", "123456789"]) {
    assert.equal(isVersion(v), true, v);
  }
  for (const v of ["", "0", "01", "-1", "1.0", "1e3", "1234567890", " 1"]) {
    assert.equal(isVersion(v), false, v);
  }
});

test("an admin API message naming a declared variable becomes a field error", () => {
  assert.deepEqual(apiFieldError(SCHEMA, "variables.amount: is required"), {
    name: "amount",
    message: "Is required.",
  });
  assert.deepEqual(
    apiFieldError(SCHEMA, "variables.name: must be at most 5 characters"),
    { name: "name", message: "Must be at most 5 characters." },
  );
  for (const m of [
    "variables: a variable is not declared by this template version",
    "variables.unknown: is required",
    "html: execution failed",
    "variables.amount:",
    "",
  ]) {
    assert.equal(apiFieldError(SCHEMA, m), undefined, m);
  }
});

test("preview failures map to fixed messages; only render errors show the API text", () => {
  assert.deepEqual(
    previewFailure(
      SCHEMA,
      422,
      "variables_invalid",
      "variables.vip: must be true or false",
    ),
    {
      error: "Check these variables:",
      fieldErrors: [{ name: "vip", message: "Must be true or false." }],
    },
  );
  assert.deepEqual(
    previewFailure(
      SCHEMA,
      422,
      "render_failed",
      "html: output is larger than 524288 bytes",
    ),
    {
      error:
        "The template could not be rendered: html: output is larger than 524288 bytes",
      fieldErrors: [],
    },
  );
  assert.deepEqual(
    previewFailure(
      SCHEMA,
      422,
      "variables_invalid",
      "variables: a variable is not declared by this template version",
    ),
    {
      error:
        "The template could not be rendered: variables: a variable is not declared by this template version",
      fieldErrors: [],
    },
  );
  const generic = "Preview is unavailable right now. Try again later.";
  for (const [status, code, want] of [
    [422, "something_new", generic],
    [404, "not_found", "This template version does not exist."],
    [413, "payload_too_large", "The variables are too large to preview."],
    [400, "invalid_json", generic],
    [503, "service_unavailable", generic],
    [500, "internal_error", generic],
    [undefined, undefined, generic],
  ] as const) {
    assert.deepEqual(
      previewFailure(SCHEMA, status, code, "raw API text"),
      { error: want, fieldErrors: [] },
      `${status} ${code}`,
    );
  }
});
