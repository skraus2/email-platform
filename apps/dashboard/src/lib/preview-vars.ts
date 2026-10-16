/**
 * Template preview variables: turns the preview form into the typed
 * `variables` object of the admin API. Types come from the version's
 * declared schema; fields of other names are ignored. Error messages never
 * repeat a value. No imports: unit-tested with node --test.
 */

export type VariableSpec = {
  type: "string" | "number" | "boolean";
  required: boolean;
  max_length?: number;
};

export type VariableSchema = Record<string, VariableSpec>;

export type VariableValue = string | number | boolean;

export type VariableError = { name: string; message: string };

/** A decimal number literal, as an operator would type it (no hex, no NaN). */
const NUMBER = /^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$/;

const VERSION = /^[1-9][0-9]{0,8}$/;

/** A template version number as it may appear in a URL. */
export function isVersion(v: string): boolean {
  return VERSION.test(v);
}

/** Declared variable names in a stable order. */
export function variableNames(schema: VariableSchema): string[] {
  return Object.keys(schema).sort();
}

/** The form field of a variable; prefixed so it cannot clash with framework fields. */
export function fieldName(name: string): string {
  return `v.${name}`;
}

/**
 * Builds the variables for a preview request. Strings are always sent (an
 * empty field is ""); an empty number field and an unset boolean are left
 * out, so the template sees the zero value or the API reports a missing
 * required variable — unless the field is required, which is reported here
 * without calling the API.
 */
export function buildPreviewVariables(
  schema: VariableSchema,
  get: (field: string) => string | null,
): { variables: Record<string, VariableValue>; errors: VariableError[] } {
  const variables: Record<string, VariableValue> = {};
  const errors: VariableError[] = [];
  for (const name of variableNames(schema)) {
    const spec = schema[name];
    const raw = get(fieldName(name)) ?? "";
    const fail = (message: string) => errors.push({ name, message });
    switch (spec.type) {
      case "string": {
        const max = spec.max_length ?? 1000;
        if ([...raw].length > max) {
          fail(`Too long: at most ${max} characters.`);
        } else {
          variables[name] = raw;
        }
        break;
      }
      case "number": {
        const text = raw.trim();
        if (text === "") {
          if (spec.required) {
            fail("Enter a number.");
          }
          break;
        }
        const n = Number(text);
        if (!NUMBER.test(text) || !Number.isFinite(n)) {
          fail("Enter a number, for example 12 or -3.5.");
        } else {
          variables[name] = n;
        }
        break;
      }
      case "boolean": {
        if (raw === "true" || raw === "false") {
          variables[name] = raw === "true";
        } else if (raw !== "" || spec.required) {
          fail("Choose true or false.");
        }
        break;
      }
    }
  }
  return { variables, errors };
}

const API_FIELD = /^variables\.([a-z][a-z0-9_]{0,39}): ([\s\S]+)$/;

/**
 * The field error in an admin API `422` message ("variables.<name>: <reason>"),
 * when it names a declared variable; such messages name variables, never
 * values.
 */
export function apiFieldError(
  schema: VariableSchema,
  message: string,
): VariableError | undefined {
  const m = API_FIELD.exec(message);
  if (!m || !Object.hasOwn(schema, m[1])) {
    return undefined;
  }
  const reason = m[2].trim();
  return {
    name: m[1],
    message: `${reason.charAt(0).toUpperCase()}${reason.slice(1)}${reason.endsWith(".") ? "" : "."}`,
  };
}

const UNAVAILABLE = "Preview is unavailable right now. Try again later.";

/**
 * What the operator sees when a preview fails, from the admin API answer
 * (status and error code; both undefined when the API was not reached).
 * Only the two documented 422 codes show the API message, which names
 * variables and template parts, never values; anything else gets a fixed
 * text.
 */
export function previewFailure(
  schema: VariableSchema,
  status: number | undefined,
  code: string | undefined,
  message: string,
): { error: string; fieldErrors: VariableError[] } {
  if (
    status === 422 &&
    (code === "variables_invalid" || code === "render_failed")
  ) {
    const field = apiFieldError(schema, message);
    return field
      ? { error: "Check these variables:", fieldErrors: [field] }
      : {
          error: `The template could not be rendered: ${message}`,
          fieldErrors: [],
        };
  }
  if (status === 404) {
    return { error: "This template version does not exist.", fieldErrors: [] };
  }
  if (status === 413) {
    return {
      error: "The variables are too large to preview.",
      fieldErrors: [],
    };
  }
  return { error: UNAVAILABLE, fieldErrors: [] };
}
