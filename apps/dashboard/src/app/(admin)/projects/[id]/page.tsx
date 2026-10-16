import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import { None } from "@/components/none";
import { Status } from "@/components/status";
import { TemplatesTable } from "@/components/templates-table";
import { Timestamp } from "@/components/timestamp";
import {
  adminFetchAll,
  type Sender,
  type Template,
  type WebhookEndpoint,
} from "@/lib/admin-api";
import { findProject } from "@/lib/admin-data";
import { isUUID } from "@/lib/email-filters";
import { shorten } from "@/lib/format";

/** Rows loaded per sub-list (templates, senders, endpoints). */
const MAX_ROWS = 500;

export async function generateMetadata({
  params,
}: PageProps<"/projects/[id]">): Promise<Metadata> {
  const { id } = await params;
  const project = isUUID(id) ? await findProject(id.toLowerCase()) : undefined;
  return {
    title: project
      ? `Project: ${shorten(project.name, 60)}`
      : "Project not found",
  };
}

export default async function ProjectPage({
  params,
}: PageProps<"/projects/[id]">) {
  const { id } = await params;
  if (!isUUID(id)) {
    notFound();
  }
  const pid = id.toLowerCase();
  const filter = { project_id: pid };
  const [project, templates, senders, endpoints] = await Promise.all([
    findProject(pid),
    adminFetchAll<Template>("/admin/v1/templates", MAX_ROWS, filter),
    adminFetchAll<Sender>("/admin/v1/senders", MAX_ROWS, filter),
    adminFetchAll<WebhookEndpoint>(
      "/admin/v1/webhook-endpoints",
      MAX_ROWS,
      filter,
    ),
  ]);
  if (!project) {
    notFound();
  }

  return (
    <>
      <p>
        <Link href="/projects">All projects</Link>
      </p>
      <h1 className="wrap">{project.name}</h1>
      <p className="muted">
        Project <code>{project.id}</code>
      </p>

      <section aria-labelledby="details-h">
        <h2 id="details-h">Details</h2>
        <dl className="meta">
          <dt>Status</dt>
          <dd>
            <Status value={project.status} />
          </dd>
          <dt>Created</dt>
          <dd>
            <Timestamp iso={project.created_at} />
          </dd>
          <dt>Messages</dt>
          <dd>
            <Link href={`/emails?project_id=${encodeURIComponent(project.id)}`}>
              Emails of this project
            </Link>
          </dd>
        </dl>
      </section>

      <section aria-labelledby="senders-h">
        <h2 id="senders-h">Sender identities</h2>
        {senders.rows.length === 0 ? (
          <p className="empty">No sender identities.</p>
        ) : (
          <div
            className="table-wrap"
            role="region"
            aria-labelledby="senders-h"
            tabIndex={0}
          >
            <table>
              <caption className="visually-hidden">
                Sender addresses and domains of this project
              </caption>
              <thead>
                <tr>
                  <th scope="col">Address or domain</th>
                  <th scope="col">Kind</th>
                  <th scope="col">Status</th>
                  <th scope="col">Verified</th>
                  <th scope="col">Created</th>
                </tr>
              </thead>
              <tbody>
                {senders.rows.map((s) => (
                  <tr key={s.id}>
                    <td className="wrap">{s.value}</td>
                    <td>{s.kind}</td>
                    <td>
                      <Status value={s.status} />
                    </td>
                    <td>
                      <Timestamp iso={s.verified_at} />
                    </td>
                    <td>
                      <Timestamp iso={s.created_at} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {senders.more ? <MoreRows /> : null}
      </section>

      <section aria-labelledby="endpoints-h">
        <h2 id="endpoints-h">Webhook endpoints</h2>
        {endpoints.rows.length === 0 ? (
          <p className="empty">No webhook endpoints.</p>
        ) : (
          <div
            className="table-wrap"
            role="region"
            aria-labelledby="endpoints-h"
            tabIndex={0}
          >
            <table>
              <caption className="visually-hidden">
                Webhook endpoints of this project (signing secrets are never
                shown)
              </caption>
              <thead>
                <tr>
                  <th scope="col">URL</th>
                  <th scope="col">Event types</th>
                  <th scope="col">Status</th>
                  <th scope="col">Created</th>
                  <th scope="col">Updated</th>
                </tr>
              </thead>
              <tbody>
                {endpoints.rows.map((e) => (
                  <tr key={e.id}>
                    <td className="wrap">{e.url}</td>
                    <td>
                      {e.event_types.length === 0 ? (
                        <None />
                      ) : (
                        <ul className="plain">
                          {e.event_types.map((t) => (
                            <li key={t}>{t}</li>
                          ))}
                        </ul>
                      )}
                    </td>
                    <td>
                      <Status value={e.status} />
                    </td>
                    <td>
                      <Timestamp iso={e.created_at} />
                    </td>
                    <td>
                      <Timestamp iso={e.updated_at} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {endpoints.more ? <MoreRows /> : null}
      </section>

      <section aria-labelledby="templates-h">
        <h2 id="templates-h">Templates</h2>
        {templates.rows.length === 0 ? (
          <p className="empty">No templates.</p>
        ) : (
          <TemplatesTable
            templates={templates.rows}
            labelId="templates-h"
            caption="Templates of this project, newest first"
          />
        )}
        {templates.more ? <MoreRows /> : null}
      </section>
    </>
  );
}

function MoreRows() {
  return (
    <p className="notice">
      Only the first {MAX_ROWS} rows are shown; more exist.
    </p>
  );
}
