import type { Metadata } from "next";
import Link from "next/link";
import { notFound, unstable_rethrow } from "next/navigation";
import { cache } from "react";
import { Status } from "@/components/status";
import { Timestamp } from "@/components/timestamp";
import {
  AdminAPIError,
  adminFetch,
  type AutomationRunDetail,
} from "@/lib/admin-api";
import { isUUID } from "@/lib/email-filters";

const loadRun = cache((id: string) =>
  adminFetch<AutomationRunDetail>(
    `/admin/v1/automation-runs/${encodeURIComponent(id)}`,
  ),
);

export async function generateMetadata({
  params,
}: PageProps<"/automation-runs/[id]">): Promise<Metadata> {
  const { id } = await params;
  return { title: isUUID(id) ? "Automation run" : "Run not found" };
}

export default async function RunPage({
  params,
}: PageProps<"/automation-runs/[id]">) {
  const { id } = await params;
  if (!isUUID(id)) {
    notFound();
  }
  let r: AutomationRunDetail;
  try {
    r = await loadRun(id.toLowerCase());
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof AdminAPIError && err.status === 404) {
      notFound();
    }
    throw err;
  }

  return (
    <>
      <p>
        <Link href={`/automations/${encodeURIComponent(r.automation_id)}`}>
          Automation {r.automation_name}
        </Link>
      </p>
      <h1>Automation run</h1>
      <p className="muted">
        Run <code>{r.id}</code>
      </p>

      <section aria-labelledby="details-h">
        <h2 id="details-h">Details</h2>
        <dl className="meta">
          <dt>Status</dt>
          <dd>
            <Status value={r.status} />
            {r.reason ? <span className="muted"> ({r.reason})</span> : null}
          </dd>
          <dt>Project</dt>
          <dd className="wrap">
            <Link
              href={`/projects/${encodeURIComponent(r.project_id)}`}
              prefetch={false}
            >
              {r.project_name}
            </Link>
          </dd>
          <dt>Contact</dt>
          <dd>
            <code>{r.contact_id}</code>
          </dd>
          <dt>Next step</dt>
          <dd>
            {r.status === "active" ? (
              <>
                step {r.step + 1}, <Timestamp iso={r.next_step_at} />
              </>
            ) : (
              <span className="muted">none (the run ended)</span>
            )}
          </dd>
          <dt>Started</dt>
          <dd>
            <Timestamp iso={r.created_at} />
          </dd>
          <dt>Ended</dt>
          <dd>
            <Timestamp iso={r.finished_at} />
          </dd>
        </dl>
      </section>

      <section aria-labelledby="emails-h">
        <h2 id="emails-h">Emails</h2>
        {r.emails.length === 0 ? (
          <p className="empty">This run has not sent an email.</p>
        ) : (
          <div
            className="table-wrap"
            role="region"
            aria-labelledby="emails-caption"
            tabIndex={0}
          >
            <table>
              <caption id="emails-caption" className="visually-hidden">
                Emails of this run by step
              </caption>
              <thead>
                <tr>
                  <th scope="col">Step</th>
                  <th scope="col">Email</th>
                  <th scope="col">Status</th>
                </tr>
              </thead>
              <tbody>
                {r.emails.map((e) => (
                  <tr key={e.id}>
                    <td>{e.step + 1}</td>
                    <td>
                      <Link
                        href={`/emails/${encodeURIComponent(e.id)}`}
                        prefetch={false}
                        aria-label={`Email ${e.id}`}
                      >
                        <code>{e.id}</code>
                      </Link>
                    </td>
                    <td>
                      <Status value={e.status} />
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
