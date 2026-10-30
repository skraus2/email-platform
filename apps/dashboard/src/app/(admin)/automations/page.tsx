import type { Metadata } from "next";
import Link from "next/link";
import { Status } from "@/components/status";
import { Timestamp } from "@/components/timestamp";
import { adminFetchAll, type Automation } from "@/lib/admin-api";

export const metadata: Metadata = { title: "Automations" };

/** Automations loaded for this view. */
const AUTOMATION_CAP = 500;

export default async function AutomationsPage() {
  // Also the session check of this page (see adminFetch).
  const automations = await adminFetchAll<Automation>(
    "/admin/v1/automations",
    AUTOMATION_CAP,
  );

  return (
    <>
      <h1>Automations</h1>
      <p className="muted">
        Automations of all projects, newest first. Each event an app reports for
        a contact starts a run of every active automation with that trigger.
        Times are in UTC.
      </p>
      {automations.more ? (
        <p className="notice">
          Only the first {AUTOMATION_CAP} automations are shown; more exist.
        </p>
      ) : null}
      {automations.rows.length === 0 ? (
        <p className="empty">No automations yet.</p>
      ) : (
        <div
          className="table-wrap"
          role="region"
          aria-labelledby="automations-caption"
          tabIndex={0}
        >
          <table>
            <caption id="automations-caption">
              {automations.rows.length} automation
              {automations.rows.length === 1 ? "" : "s"}, newest first
            </caption>
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">Project</th>
                <th scope="col">Status</th>
                <th scope="col">Trigger</th>
                <th scope="col">Steps</th>
                <th scope="col">Created</th>
              </tr>
            </thead>
            <tbody>
              {automations.rows.map((a) => (
                <tr key={a.id}>
                  <td className="wrap">
                    <Link
                      href={`/automations/${encodeURIComponent(a.id)}`}
                      prefetch={false}
                    >
                      {a.name}
                    </Link>
                  </td>
                  <td className="wrap">{a.project_name}</td>
                  <td>
                    <Status value={a.status} />
                  </td>
                  <td className="wrap">
                    <code>{a.trigger_event}</code>
                  </td>
                  <td>{a.step_count}</td>
                  <td>
                    <Timestamp iso={a.created_at} />
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
