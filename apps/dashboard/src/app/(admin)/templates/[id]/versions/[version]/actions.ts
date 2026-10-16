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
  buildPreviewVariables,
  isVersion,
  previewFailure,
  type VariableError,
} from "@/lib/preview-vars";

export type PreviewOutcome =
  | { result: Rendered; error?: undefined; fieldErrors: [] }
  | { result?: undefined; error: string; fieldErrors: VariableError[] };

/**
 * Renders a template version with the entered variables at the admin API.
 * Nothing is stored or sent. The variables travel only in POST bodies (this
 * action and the admin API request), never in a URL, and are not logged.
 * The bound id and version come from the client, so they are validated
 * here; variable types come from the server's copy of the version. Only the
 * form is sent in, so a large earlier result never travels back.
 */
export async function previewTemplate(
  templateId: unknown,
  version: unknown,
  form: FormData,
): Promise<PreviewOutcome> {
  if (
    typeof templateId !== "string" ||
    typeof version !== "string" ||
    !isUUID(templateId) ||
    !isVersion(version)
  ) {
    return previewFailure({}, 404, "not_found", "");
  }
  const path = `/admin/v1/templates/${encodeURIComponent(templateId.toLowerCase())}/versions/${version}`;

  let schema: TemplateVersion["variables"];
  try {
    schema = (await adminFetch<TemplateVersion>(path)).variables;
  } catch (err) {
    unstable_rethrow(err);
    return failed({}, err);
  }

  const { variables, errors } = buildPreviewVariables(schema, (f) => {
    const v = form.get(f);
    return typeof v === "string" ? v : null;
  });
  if (errors.length > 0) {
    return { error: "Check these variables:", fieldErrors: errors };
  }

  try {
    const result = await adminFetch<Rendered>(`${path}/preview`, {
      method: "POST",
      body: { variables },
    });
    return { result, fieldErrors: [] };
  } catch (err) {
    unstable_rethrow(err);
    return failed(schema, err);
  }
}

function failed(
  schema: TemplateVersion["variables"],
  err: unknown,
): PreviewOutcome {
  return err instanceof AdminAPIError
    ? previewFailure(schema, err.status, err.code, err.message)
    : previewFailure(schema, undefined, undefined, "");
}
