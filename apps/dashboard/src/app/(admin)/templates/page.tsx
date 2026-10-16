import type { Metadata } from "next";
import Link from "next/link";
import { TemplatesTable } from "@/components/templates-table";
import { adminFetchAll, type Template } from "@/lib/admin-api";
import { loadProjects } from "@/lib/admin-data";

export const metadata: Metadata = { title: "Templates" };

/** Templates loaded for this view. */
const TEMPLATE_CAP = 500;

export default async function TemplatesPage() {
  // Also the session check of this page (see adminFetch).
  const [projects, templates] = await Promise.all([
    loadProjects(),
    adminFetchAll<Template>("/admin/v1/templates", TEMPLATE_CAP),
  ]);

  // One group per project, in the order of the project list; templates of
  // projects beyond the loaded project list still get a group of their own.
  const groups = new Map<string, { name: string; rows: Template[] }>();
  for (const p of projects.rows) {
    groups.set(p.id, { name: p.name, rows: [] });
  }
  for (const t of templates.rows) {
    const g = groups.get(t.project_id);
    if (g) {
      g.rows.push(t);
    } else {
      groups.set(t.project_id, {
        name: `Project ${t.project_id}`,
        rows: [t],
      });
    }
  }
  const filled = [...groups.entries()].filter(([, g]) => g.rows.length > 0);

  return (
    <>
      <h1>Templates</h1>
      <p className="muted">
        Templates of all projects, grouped by project, newest first. Times are
        in UTC.
      </p>
      {templates.more ? (
        <p className="notice">
          Only the first {TEMPLATE_CAP} templates are shown; more exist.
        </p>
      ) : null}
      {filled.length === 0 ? (
        <p className="empty">No templates yet.</p>
      ) : (
        filled.map(([projectId, g], i) => (
          <section key={projectId} aria-labelledby={`project-${i}-h`}>
            <h2 id={`project-${i}-h`} className="wrap">
              {g.name}
            </h2>
            <p>
              <Link
                href={`/projects/${encodeURIComponent(projectId)}`}
                prefetch={false}
              >
                Project details
                <span className="visually-hidden">: {g.name}</span>
              </Link>
            </p>
            <TemplatesTable
              templates={g.rows}
              labelId={`project-${i}-h`}
              caption={`Templates of ${g.name}, newest first`}
            />
          </section>
        ))
      )}
    </>
  );
}
