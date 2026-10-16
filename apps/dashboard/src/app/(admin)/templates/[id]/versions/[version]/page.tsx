import type { Metadata } from "next";
import Link from "next/link";
import { notFound, unstable_rethrow } from "next/navigation";
import { None } from "@/components/none";
import { displayName } from "@/components/templates-table";
import { Timestamp } from "@/components/timestamp";
import {
  AdminAPIError,
  type TemplateDetail,
  type TemplateVersion,
} from "@/lib/admin-api";
import { loadTemplate, loadTemplateVersion } from "@/lib/admin-data";
import { isUUID } from "@/lib/email-filters";
import { shorten } from "@/lib/format";
import { isVersion, variableNames } from "@/lib/preview-vars";
import { previewTemplate } from "./actions";
import { PreviewForm } from "./preview-form";

type Props = PageProps<"/templates/[id]/versions/[version]">;

/** Loads the template and the version; undefined for a malformed id or number. */
async function load(params: Props["params"]) {
  const { id, version } = await params;
  if (!isUUID(id) || !isVersion(version)) {
    return undefined;
  }
  const tid = id.toLowerCase();
  return Promise.all([
    loadTemplate(tid),
    loadTemplateVersion(tid, Number(version)),
  ]);
}

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  try {
    const loaded = await load(params);
    if (!loaded) {
      return { title: "Template version not found" };
    }
    const [t, v] = loaded;
    return {
      title: `Template: ${shorten(displayName(t.name), 60)}, version ${v.version}`,
    };
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof AdminAPIError && err.status === 404) {
      return { title: "Template version not found" };
    }
    throw err;
  }
}

export default async function VersionPage({ params }: Props) {
  let t: TemplateDetail;
  let v: TemplateVersion;
  try {
    const loaded = await load(params);
    if (!loaded) {
      notFound();
    }
    [t, v] = loaded;
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof AdminAPIError && err.status === 404) {
      notFound();
    }
    throw err;
  }
  const base = `/templates/${encodeURIComponent(t.id)}`;
  const names = variableNames(v.variables);
  const newer =
    t.latest_version !== null && t.latest_version > v.version
      ? t.latest_version
      : undefined;

  return (
    <>
      <p>
        <Link href={base}>
          All versions<span className="visually-hidden"> of this template</span>
        </Link>
      </p>
      <h1 className="wrap">
        {displayName(t.name)}, version {v.version}
      </h1>
      <p className="muted">
        Published <Timestamp iso={v.created_at} /> · template{" "}
        <code>{t.id}</code>
      </p>
      {newer ? (
        <p className="notice">
          This is an older version.{" "}
          <Link href={`${base}/versions/${newer}`} prefetch={false}>
            Latest version: {newer}
          </Link>
        </p>
      ) : null}

      <section aria-labelledby="variables-h">
        <h2 id="variables-h">Variables</h2>
        {names.length === 0 ? (
          <p className="empty">This version declares no variables.</p>
        ) : (
          <div
            className="table-wrap"
            role="region"
            aria-labelledby="variables-h"
            tabIndex={0}
          >
            <table>
              <caption className="visually-hidden">Declared variables</caption>
              <thead>
                <tr>
                  <th scope="col">Name</th>
                  <th scope="col">Type</th>
                  <th scope="col">Required</th>
                  <th scope="col">Max length</th>
                </tr>
              </thead>
              <tbody>
                {names.map((n) => {
                  const spec = v.variables[n];
                  return (
                    <tr key={n}>
                      <th scope="row">
                        <code>{n}</code>
                      </th>
                      <td>{spec.type}</td>
                      <td>{spec.required ? "yes" : "no"}</td>
                      <td>
                        {spec.type === "string" ? (
                          `${spec.max_length ?? 1000} characters`
                        ) : (
                          <None />
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section aria-labelledby="source-h">
        <h2 id="source-h">Source</h2>
        <p className="muted">Exactly as published.</p>
        <Source id="src-subject" label="Subject" value={v.subject} />
        <Source id="src-text" label="Text part" value={v.text} />
        <Source id="src-html" label="HTML part" value={v.html} />
      </section>

      <section aria-labelledby="preview-h">
        <h2 id="preview-h">Preview</h2>
        <p className="muted">
          Renders this version with the values below. Nothing is stored or sent,
          and the values are not kept.
        </p>
        <PreviewForm
          action={previewTemplate.bind(null, t.id, String(v.version))}
          schema={v.variables}
        />
      </section>
    </>
  );
}

/** A source part as text, scrollable when long; never interpreted as HTML. */
function Source({
  id,
  label,
  value,
}: {
  id: string;
  label: string;
  value: string | null;
}) {
  return (
    <>
      <h3 id={id}>{label}</h3>
      {value === null ? (
        <p className="empty">This version has no HTML part.</p>
      ) : value === "" ? (
        <p className="empty">Empty.</p>
      ) : (
        <div className="source" role="region" aria-labelledby={id} tabIndex={0}>
          <pre>{value}</pre>
        </div>
      )}
    </>
  );
}
