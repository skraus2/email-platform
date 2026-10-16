"use server";

import { unstable_rethrow } from "next/navigation";
import {
  AdminAPIError,
  adminFetch,
  type Rendered,
  type TemplateVersion,
} from "@/lib/admin-api";
import { isUUID } from "@/lib/email-filters";
import {
  apiFieldError,
  buildPreviewVariables,
  isVersion,
  type VariableError,
} from "@/lib/preview-vars";

export type PreviewState =
  | {
      /** Changes on every submit, so the form refills and messages are re-announced. */
      attempt: number;
      /** Entered values, to refill the form after React resets it. */
      values: Record<string, string>;
      result?: Rendered;
      error?: string;
      fieldErrors: VariableError[];
    }
  | undefined;

/**
 * Renders a template version with the entered variables at the admin API.
 * Nothing is stored or sent. The variables travel only in POST bodies (this
 * action and the admin API request), never in a URL, and are not logged.
 * The bound id and version come from the client, so they are validated
 * here; variable types come from the server's copy of the version.
 */
export async function previewTemplate(
  templateId: unknown,
  version: unknown,
  prev: PreviewState,
  form: FormData,
): Promise<PreviewState> {
  const attempt = (prev?.attempt ?? 0) + 1;
  const fail = (
    error: string,
    values: Record<string, string> = {},
    fieldErrors: VariableError[] = [],
  ): PreviewState => ({ attempt, values, error, fieldErrors });

  if (
    typeof templateId !== "string" ||
    typeof version !== "string" ||
    !isUUID(templateId) ||
    !isVersion(version)
  ) {
    return fail("This template version does not exist.");
  }
  const path = `/admin/v1/templates/${encodeURIComponent(templateId.toLowerCase())}/versions/${version}`;

  let schema: TemplateVersion["variables"];
  try {
    schema = (await adminFetch<TemplateVersion>(path)).variables;
  } catch (err) {
    unstable_rethrow(err);
    return fail(failureMessage(err));
  }

  const { variables, errors, values } = buildPreviewVariables(schema, (f) => {
    const v = form.get(f);
    return typeof v === "string" ? v : null;
  });
  if (errors.length > 0) {
    return fail("Check the highlighted variables.", values, errors);
  }

  try {
    const result = await adminFetch<Rendered>(`${path}/preview`, {
      method: "POST",
      body: { variables },
    });
    return { attempt, values, result, fieldErrors: [] };
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof AdminAPIError && err.status === 422) {
      const field = apiFieldError(schema, err.message);
      return field
        ? fail("Check the highlighted variables.", values, [field])
        : fail(`The template could not be rendered: ${err.message}`, values);
    }
    return fail(failureMessage(err), values);
  }
}

function failureMessage(err: unknown): string {
  if (err instanceof AdminAPIError) {
    if (err.status === 404) {
      return "This template version does not exist.";
    }
    if (err.status === 413) {
      return "The variables are too large to preview.";
    }
  }
  return "Preview is unavailable right now. Try again later.";
}
