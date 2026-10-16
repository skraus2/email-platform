"use client";

import { useActionState } from "react";
import { displaySubject } from "@/lib/format";
import {
  fieldName,
  variableNames,
  type VariableSchema,
  type VariableSpec,
} from "@/lib/preview-vars";
import type { PreviewState } from "./actions";

/**
 * The preview form: one input per declared variable, posted to a Server
 * Action (values never enter the URL). The rendered HTML is shown only in
 * a sandboxed frame without any allow token: no scripts, no forms, no
 * popups, an opaque origin, and the page's CSP (no remote images).
 */
export function PreviewForm({
  action,
  schema,
}: {
  action: (prev: PreviewState, form: FormData) => Promise<PreviewState>;
  schema: VariableSchema;
}) {
  const [state, formAction, pending] = useActionState<PreviewState, FormData>(
    action,
    undefined,
  );
  const names = variableNames(schema);
  const errorOf = (name: string) =>
    state?.fieldErrors.find((e) => e.name === name)?.message;
  const result = state?.result;

  return (
    <>
      <form
        action={formAction}
        className="preview-form"
        onSubmit={(e) => {
          if (pending) {
            e.preventDefault();
          }
        }}
      >
        {names.map((name) => (
          <Field
            key={`${name}-${state?.attempt ?? 0}`}
            name={name}
            spec={schema[name]}
            value={state?.values[name] ?? ""}
            error={errorOf(name)}
          />
        ))}
        {state?.error ? (
          <div key={state.attempt} className="error" role="alert">
            <p>{state.error}</p>
            {state.fieldErrors.length > 0 ? (
              <ul>
                {state.fieldErrors.map((e) => (
                  <li key={e.name}>
                    <a href={`#var-${e.name}`}>
                      {e.name}: {e.message}
                    </a>
                  </li>
                ))}
              </ul>
            ) : null}
          </div>
        ) : null}
        <div className="actions">
          {/* Kept enabled while pending so focus stays on it; a second submit is ignored. */}
          <button type="submit" aria-disabled={pending || undefined}>
            {pending ? "Rendering…" : "Render preview"}
          </button>
        </div>
      </form>
      <p className="visually-hidden" role="status">
        {pending ? "Rendering preview…" : result ? "Preview updated." : ""}
      </p>

      <section aria-labelledby="result-h" className="preview-result">
        <h3 id="result-h">Result</h3>
        {!result ? (
          <p className="empty">Render the preview to see the result here.</p>
        ) : (
          <>
            <h4 id="result-subject">Subject</h4>
            <p className="wrap">{displaySubject(result.subject)}</p>
            <h4 id="result-text">Text part</h4>
            {result.text === "" ? (
              <p className="empty">Empty.</p>
            ) : (
              <div
                className="source"
                role="region"
                aria-labelledby="result-text"
                tabIndex={0}
              >
                <pre>{result.text}</pre>
              </div>
            )}
            <h4 id="result-html">HTML part</h4>
            {result.html === null ? (
              <p className="empty">This version has no HTML part.</p>
            ) : (
              <iframe
                key={state.attempt}
                className="preview-frame"
                sandbox=""
                srcDoc={result.html}
                title="Rendered HTML part"
                referrerPolicy="no-referrer"
              />
            )}
          </>
        )}
      </section>
    </>
  );
}

function Field({
  name,
  spec,
  value,
  error,
}: {
  name: string;
  spec: VariableSpec;
  value: string;
  error: string | undefined;
}) {
  const id = `var-${name}`;
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const common = {
    id,
    name: fieldName(name),
    defaultValue: value,
    "aria-invalid": error ? true : undefined,
    "aria-describedby": error ? `${hintId} ${errorId}` : hintId,
  } as const;
  const needed = spec.required && spec.type !== "string";

  return (
    <div className="field">
      <label htmlFor={id}>
        <code>{name}</code>
        {needed ? " (required)" : null}
      </label>
      {spec.type === "boolean" ? (
        <select {...common} required={needed}>
          <option value="">{needed ? "Choose…" : "(not set)"}</option>
          <option value="true">true</option>
          <option value="false">false</option>
        </select>
      ) : spec.type === "number" ? (
        <input
          {...common}
          inputMode="decimal"
          autoComplete="off"
          spellCheck={false}
          required={needed}
        />
      ) : (
        // No maxLength: browsers count UTF-16 units, the limit is in
        // characters; the action checks it.
        <input {...common} autoComplete="off" />
      )}
      <p className="hint" id={hintId}>
        {hint(spec)}
      </p>
      {error ? (
        <p className="error" id={errorId}>
          {error}
        </p>
      ) : null}
    </div>
  );
}

function hint(spec: VariableSpec): string {
  switch (spec.type) {
    case "string":
      return `Text, up to ${spec.max_length ?? 1000} characters; empty means empty text.`;
    case "number":
      return spec.required
        ? "A number, for example 12 or -3.5."
        : "A number, for example 12 or -3.5; leave empty to omit.";
    case "boolean":
      return spec.required ? "true or false." : "true, false or not set.";
  }
}
