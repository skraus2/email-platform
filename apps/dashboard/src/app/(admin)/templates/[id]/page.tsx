import type { Metadata } from "next";
import Link from "next/link";
import { notFound, unstable_rethrow } from "next/navigation";
import { BackLink } from "@/components/back-link";
import { None } from "@/components/none";
import { displayName } from "@/components/templates-table";
import { Timestamp } from "@/components/timestamp";
import { AdminAPIError, type TemplateDetail } from "@/lib/admin-api";
import { findProject, loadTemplate } from "@/lib/admin-data";
import { isUUID } from "@/lib/email-filters";
import { shorten } from "@/lib/format";

export async function generateMetadata({
  params,
}: PageProps<"/templates/[id]">): Promise<Metadata> {
  const { id } = await params;
  if (!isUUID(id)) {
    return { title: "Template not found" };
  }
  try {
    const t = await loadTemplate(id.toLowerCase());
    return { title: `Template: ${shorten(displayName(t.name), 60)}` };
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof AdminAPIError && err.status === 404) {
      return { title: "Template not found" };
    }
    throw err;
  }
}

export default async function TemplatePage({
  params,
}: PageProps<"/templates/[id]">) {
  const { id } = await params;
  if (!isUUID(id)) {
    notFound();
  }
  let t: TemplateDetail;
  try {
    t = await loadTemplate(id.toLowerCase());
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof AdminAPIError && err.status === 404) {
      notFound();
    }
    throw err;
  }
  const project = await findProject(t.project_id);
  const base = `/templates/${encodeURIComponent(t.id)}`;

  return (
    <>
      <BackLink href="/templates">All templates</BackLink>
      <h1 className="wrap">{displayName(t.name)}</h1>
      <p className="muted">
        Template <code>{t.id}</code>
      </p>

      <section aria-labelledby="details-h">
        <h2 id="details-h">Details</h2>
        <dl className="meta">
          <dt>Slug</dt>
          <dd className="wrap">
            <code>{t.slug}</code>
          </dd>
          <dt>Project</dt>
          <dd className="wrap">
            <Link
              href={`/projects/${encodeURIComponent(t.project_id)}`}
              prefetch={false}
            >
              {project?.name ?? t.project_id}
            </Link>
          </dd>
          <dt>Latest version</dt>
          <dd>{t.latest_version ?? <None />}</dd>
          <dt>Created</dt>
          <dd>
            <Timestamp iso={t.created_at} />
          </dd>
          <dt>Messages</dt>
          <dd>
            <Link href={`/emails?template_id=${encodeURIComponent(t.id)}`}>
              Emails sent with this template
            </Link>
          </dd>
        </dl>
      </section>

      <section aria-labelledby="versions-h">
        <h2 id="versions-h">Versions</h2>
        {t.versions.length === 0 ? (
          <p className="empty">No version has been published yet.</p>
        ) : (
          <div
            className="table-wrap"
            role="region"
            aria-labelledby="versions-h"
            tabIndex={0}
          >
            <table>
              <caption className="visually-hidden">
                All versions, newest first
              </caption>
              <thead>
                <tr>
                  <th scope="col">Version</th>
                  <th scope="col">Published</th>
                </tr>
              </thead>
              <tbody>
                {t.versions.map((v) => (
                  <tr key={v.version}>
                    <td>
                      <Link
                        href={`${base}/versions/${v.version}`}
                        prefetch={false}
                      >
                        Version {v.version}
                      </Link>
                      {v.version === t.latest_version ? " (latest)" : null}
                    </td>
                    <td>
                      <Timestamp iso={v.created_at} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </>
  );
}
