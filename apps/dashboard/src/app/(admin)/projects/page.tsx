import type { Metadata } from "next";
import Link from "next/link";
import { Status } from "@/components/status";
import { Timestamp } from "@/components/timestamp";
import { loadProjects, PROJECT_CAP } from "@/lib/admin-data";

export const metadata: Metadata = { title: "Projects" };

export default async function ProjectsPage() {
  // Also the session check of this page (see adminFetch).
  const projects = await loadProjects();

  return (
    <>
      <h1>Projects</h1>
      <p className="muted">
        Projects using the platform, with their senders, webhook endpoints and
        templates (read-only). Times are in UTC.
      </p>
      {projects.more ? (
        <p className="notice">
          Only the first {PROJECT_CAP} projects are shown; more exist.
        </p>
      ) : null}
      {projects.rows.length === 0 ? (
        <p className="empty">No projects yet.</p>
      ) : (
        <div
          className="table-wrap"
          role="region"
          aria-labelledby="projects-caption"
          tabIndex={0}
        >
          <table>
            <caption id="projects-caption">
              {projects.rows.length} project
              {projects.rows.length === 1 ? "" : "s"}
            </caption>
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">Status</th>
                <th scope="col">Id</th>
                <th scope="col">Created</th>
              </tr>
            </thead>
            <tbody>
              {projects.rows.map((p) => (
                <tr key={p.id}>
                  <td className="wrap">
                    <Link
                      href={`/projects/${encodeURIComponent(p.id)}`}
                      prefetch={false}
                    >
                      {p.name}
                    </Link>
                  </td>
                  <td>
                    <Status value={p.status} />
                  </td>
                  <td>
                    <code>{p.id}</code>
                  </td>
                  <td>
                    <Timestamp iso={p.created_at} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
