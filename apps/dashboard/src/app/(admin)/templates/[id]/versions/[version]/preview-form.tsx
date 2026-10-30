"use client";

import { unstable_rethrow } from "next/navigation";
import { startTransition, useActionState } from "react";
import { SelectField } from "@/components/select-field";
import { displaySubject } from "@/lib/format";
import {
  fieldName,
  previewFailure,
  variableEntries,
  type VariableSchema,
  type VariableSpec,
} from "@/lib/preview-vars";
import type { PreviewOutcome } from "./actions";

type PreviewState =
  | (PreviewOutcome & {
      /** Changes on every submit, so a repeated message is announced again. */
      attempt: number;
    })
  | undefined;

/**
 * The preview form: one input per declared variable, posted to a Server
 * Action (values never enter the URL). The rendered HTML is shown only in
 * a sandboxed frame without any allow token: no scripts, no forms, no
 * popups, an opaque origin, and the page's CSP (no remote images).
 *
 * The form is submitted from onSubmit rather than through its action prop:
 * React resets action-prop forms after each submit, which would clear the
 * entered values and drop focus from the field Enter was pressed in. Only
 * the form data goes to the server, never the previous result. Before
 * hydration the form posts to the page itself (method=post), so values
 * stay out of the URL even then.
 */
export function PreviewForm({
  action,
  schema,
}: {
  action: (form: FormData) => Promise<PreviewOutcome>;
  schema: VariableSchema;
}) {
  const [state, dispatch, pending] = useActionState<PreviewState, FormData>(
    async (prev, form) => {
      const attempt = (prev?.attempt ?? 0) + 1;
      try {
        return { ...(await action(form)), attempt };
      } catch (err) {
        // A redirect (session ended) is handled by the router; anything
        // else is the request failing.
        unstable_rethrow(err);
        return { ...previewFailure({}, undefined, undefined, ""), attempt };
      }
    },
    undefined,
  );
  const entries = variableEntries(schema);
  const errorOf = (name: string) =>
    state?.fieldErrors.find((e) => e.name === name)?.message;
  const result = state?.result;

  return (
    <>
      <form
        method="post"
        className="preview-form"
        onSubmit={(e) => {
          e.preventDefault();
          if (pending) {
            return;
          }
          const form = new FormData(e.currentTarget);
          startTransition(() => {
            dispatch(form);
          });
        }}
      >
        {entries.map(([name, spec]) => (
          <Field key={name} name={name} spec={spec} error={errorOf(name)} />
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
          {/* Kept enabled while pending so focus stays on it; a second
              submit is ignored. The label stays the same: the status
              below announces the progress. */}
          <button
            type="submit"
            className="primary"
            aria-disabled={pending || undefined}
          >
            Render preview
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
            <h4 id="result-subject">Rendered subject</h4>
            <p className="source wrap">{displaySubject(result.subject)}</p>
            <h4 id="result-text">Rendered text part</h4>
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
            <h4 id="result-html">Rendered HTML part</h4>
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
  error,
}: {
  name: string;
  spec: VariableSpec;
  error: string | undefined;
}) {
  const id = `var-${name}`;
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const common = {
    id,
    name: fieldName(name),
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
        <SelectField
          {...common}
          label={name}
          options={[
            { value: "", label: needed ? "Choose…" : "(not set)" },
            { value: "true", label: "true" },
            { value: "false", label: "false" },
          ]}
          defaultValue=""
          required={needed}
        />
      ) : spec.type === "number" ? (
        <input
          {...common}
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
        ? "A number with a dot for decimals, for example 12 or -3.5."
        : "A number with a dot for decimals, for example 12 or -3.5; leave empty to omit.";
    case "boolean":
      return spec.required ? "true or false." : "true, false or not set.";
  }
}
