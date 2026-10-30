import type { Metadata } from "next";
import Link from "next/link";
import { notFound, unstable_rethrow } from "next/navigation";
import { cache } from "react";
import { BackLink } from "@/components/back-link";
import { RunPosition, RunStatus } from "@/components/run-state";
import { Status } from "@/components/status";
import { Timestamp } from "@/components/timestamp";
import {
  AdminAPIError,
  adminFetch,
  type AutomationDetail,
  type AutomationRun,
} from "@/lib/admin-api";
import { describeStep, RUN_STATUSES } from "@/lib/automations";
import { isUUID } from "@/lib/email-filters";
import { shorten } from "@/lib/format";

/** Runs shown on this page (newest first). */
const RUN_PAGE = 50;

const loadAutomation = cache((id: string) =>
  adminFetch<AutomationDetail>(
    `/admin/v1/automations/${encodeURIComponent(id)}`,
  ),
);

export async function generateMetadata({
  params,
}: PageProps<"/automations/[id]">): Promise<Metadata> {
  const { id } = await params;
  if (!isUUID(id)) {
    return { title: "Automation not found" };
  }
  try {
    const a = await loadAutomation(id.toLowerCase());
    return { title: `Automation: ${shorten(a.name, 60)}` };
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof AdminAPIError && err.status === 404) {
      return { title: "Automation not found" };
    }
    throw err;
  }
}

export default async function AutomationPage({
  params,
}: PageProps<"/automations/[id]">) {
  const { id } = await params;
  if (!isUUID(id)) {
    notFound();
  }
  let a: AutomationDetail;
  let runs: { data: AutomationRun[]; next_cursor: string | null };
  try {
    a = await loadAutomation(id.toLowerCase());
    runs = await adminFetch(
      `/admin/v1/automations/${encodeURIComponent(a.id)}/runs`,
      { query: new URLSearchParams({ limit: String(RUN_PAGE) }) },
    );
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof AdminAPIError && err.status === 404) {
      notFound();
    }
    throw err;
  }

  return (
    <>
      <BackLink href="/automations">All automations</BackLink>
      <h1 className="wrap">{a.name}</h1>
      <p className="muted">
        Automation <code>{a.id}</code>
      </p>

      <section aria-labelledby="details-h">
        <h2 id="details-h">Details</h2>
        <dl className="meta">
          <dt>Status</dt>
          <dd>
            <Status value={a.status} />
          </dd>
          <dt>Project</dt>
          <dd className="wrap">
            <Link
              href={`/projects/${encodeURIComponent(a.project_id)}`}
              prefetch={false}
            >
              {a.project_name}
            </Link>
          </dd>
          <dt>Trigger event</dt>
          <dd className="wrap">
            <code>{a.trigger_event}</code>
          </dd>
          <dt>Runs per contact</dt>
          <dd>
            {a.allow_reentry
              ? "Several, one at a time (a new run can start once the previous one ended)"
              : "One, ever"}
          </dd>
          <dt>Created</dt>
          <dd>
            <Timestamp iso={a.created_at} />
          </dd>
          <dt>Updated</dt>
          <dd>
            <Timestamp iso={a.updated_at} />
          </dd>
        </dl>
      </section>

      <section aria-labelledby="steps-h">
        <h2 id="steps-h">Steps</h2>
        <ol>
          {a.steps.map((step, i) => (
            <li key={i} className="wrap">
              {step.type === "send" ? (
                <>
                  Send{" "}
                  <Link
                    href={`/templates/${encodeURIComponent(step.template_id)}/versions/${encodeURIComponent(String(step.template_version))}`}
                    prefetch={false}
                  >
                    version {step.template_version}
                  </Link>{" "}
                  of template{" "}
                  <Link
                    href={`/templates/${encodeURIComponent(step.template_id)}`}
                    prefetch={false}
                  >
                    <code>{step.template_id}</code>
                  </Link>{" "}
                  from {step.from}
                </>
              ) : (
                describeStep(step)
              )}
            </li>
          ))}
        </ol>
      </section>

      <section aria-labelledby="counts-h">
        <h2 id="counts-h">Runs by status</h2>
        <div
          className="table-wrap"
          role="region"
          aria-labelledby="counts-caption"
          tabIndex={0}
        >
          <table>
            <caption id="counts-caption" className="visually-hidden">
              Runs by status
            </caption>
            <thead>
              <tr>
                <th scope="col">Status</th>
                <th scope="col">Runs</th>
              </tr>
            </thead>
            <tbody>
              {RUN_STATUSES.map((st) => (
                <tr key={st}>
                  <th scope="row">
                    <Status value={st} />
                  </th>
                  <td>{(a.run_counts[st] ?? 0).toLocaleString("en")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section aria-labelledby="runs-h">
        <h2 id="runs-h">Latest runs</h2>
        {runs.data.length === 0 ? (
          <p className="empty">
            {a.status === "active"
              ? "No runs yet; a run starts when an app reports the trigger event for a contact."
              : a.status === "paused"
                ? "No runs; a paused automation starts none until it is active again."
                : "No runs; an archived automation starts none."}
          </p>
        ) : (
          <>
            {runs.next_cursor ? (
              <p className="notice">
                Only the latest {RUN_PAGE} runs are shown; more exist.
              </p>
            ) : null}
            <div
              className="table-wrap"
              role="region"
              aria-labelledby="runs-caption"
              tabIndex={0}
            >
              <table>
                <caption id="runs-caption" className="visually-hidden">
                  Latest runs, newest first
                </caption>
                <thead>
                  <tr>
                    <th scope="col">Run</th>
                    <th scope="col">Status</th>
                    <th scope="col">Position</th>
                    <th scope="col">Started</th>
                    <th scope="col">Ended</th>
                  </tr>
                </thead>
                <tbody>
                  {runs.data.map((r) => (
                    <tr key={r.id}>
                      <td>
                        <Link
                          href={`/automation-runs/${encodeURIComponent(r.id)}`}
                          prefetch={false}
                          aria-label={`Run ${r.id}`}
                        >
                          <code>{r.id}</code>
                        </Link>
                      </td>
                      <td>
                        <RunStatus run={r} />
                      </td>
                      <td>
                        <RunPosition run={r} steps={a.steps.length} />
                      </td>
                      <td>
                        <Timestamp iso={r.created_at} />
                      </td>
                      <td>
                        <Timestamp iso={r.finished_at} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}
      </section>
    </>
  );
}
