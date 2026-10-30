import type { Metadata } from "next";
import Link from "next/link";
import { notFound, unstable_rethrow } from "next/navigation";
import { cache } from "react";
import { BackLink } from "@/components/back-link";
import { None } from "@/components/none";
import { Status } from "@/components/status";
import { Timestamp } from "@/components/timestamp";
import { formatUTC } from "@/lib/format";
import {
  AdminAPIError,
  adminFetch,
  adminFetchAll,
  type AdminEmail,
  type EmailEvent,
  type WebhookDelivery,
} from "@/lib/admin-api";
import { isUUID } from "@/lib/email-filters";
import { displaySubject, shorten } from "@/lib/format";

/** The email of this request; shared by the title and the page. */
const loadEmail = cache((id: string) =>
  adminFetch<AdminEmail>(`/admin/v1/emails/${encodeURIComponent(id)}`),
);

export async function generateMetadata({
  params,
}: PageProps<"/emails/[id]">): Promise<Metadata> {
  const { id } = await params;
  if (!isUUID(id)) {
    return { title: "Email not found" };
  }
  try {
    const email = await loadEmail(id.toLowerCase());
    return { title: `Email: ${shorten(displaySubject(email.subject), 60)}` };
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof AdminAPIError && err.status === 404) {
      return { title: "Email not found" };
    }
    throw err;
  }
}

/** Rows loaded per sub-list (events, deliveries). */
const MAX_ROWS = 500;

export default async function EmailPage({ params }: PageProps<"/emails/[id]">) {
  const { id } = await params;
  if (!isUUID(id)) {
    notFound();
  }
  const base = `/admin/v1/emails/${encodeURIComponent(id.toLowerCase())}`;
  let email: AdminEmail;
  let events: { rows: EmailEvent[]; more: boolean };
  let deliveries: { rows: WebhookDelivery[]; more: boolean };
  try {
    [email, events, deliveries] = await Promise.all([
      loadEmail(id.toLowerCase()),
      adminFetchAll<EmailEvent>(`${base}/events`, MAX_ROWS),
      adminFetchAll<WebhookDelivery>(`${base}/webhook-deliveries`, MAX_ROWS),
    ]);
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof AdminAPIError && err.status === 404) {
      notFound();
    }
    throw err;
  }

  return (
    <>
      <BackLink href="/emails">All messages</BackLink>
      <h1 className="wrap">{displaySubject(email.subject)}</h1>
      <p className="muted">
        Email <code>{email.id}</code>
      </p>

      <section aria-labelledby="details-h">
        <h2 id="details-h">Details</h2>
        <dl className="meta">
          <dt>Status</dt>
          <dd>
            <Status value={email.status} />
          </dd>
          <dt>Error code</dt>
          <dd>{email.error_code ?? <None />}</dd>
          <dt>Project</dt>
          <dd>
            <Link
              href={`/projects/${encodeURIComponent(email.project_id)}`}
              prefetch={false}
            >
              {email.project_name}
            </Link>{" "}
            <Link
              href={`/emails?project_id=${encodeURIComponent(email.project_id)}`}
            >
              (all emails of this project)
            </Link>
          </dd>
          <dt>From</dt>
          <dd className="wrap">{email.from}</dd>
          <dt>To</dt>
          <dd className="wrap">{email.to}</dd>
          <dt>Provider</dt>
          <dd>{email.provider ?? <None />}</dd>
          <dt>Provider message id</dt>
          <dd className="wrap">
            {email.provider_id ? <code>{email.provider_id}</code> : <None />}
          </dd>
          <dt>Attempts</dt>
          <dd>{email.attempts}</dd>
          <dt>Template</dt>
          <dd>
            {email.template_id && email.template_version !== null ? (
              <>
                <Link
                  href={`/templates/${encodeURIComponent(email.template_id)}/versions/${email.template_version}`}
                  prefetch={false}
                >
                  Version {email.template_version}
                </Link>{" "}
                of template{" "}
                <Link
                  href={`/templates/${encodeURIComponent(email.template_id)}`}
                  prefetch={false}
                >
                  <code>{email.template_id}</code>
                </Link>
              </>
            ) : (
              <span className="muted">none (raw content)</span>
            )}
          </dd>
          <dt>Broadcast</dt>
          <dd>
            {email.broadcast_id ? (
              <Link
                href={`/broadcasts/${encodeURIComponent(email.broadcast_id)}`}
                prefetch={false}
                aria-label={`Broadcast ${email.broadcast_id}`}
              >
                <code>{email.broadcast_id}</code>
              </Link>
            ) : (
              <span className="muted">
                {email.automation_id ? "none" : "none (single send)"}
              </span>
            )}
          </dd>
          <dt>Automation run</dt>
          <dd>
            {email.automation_run_id ? (
              <Link
                href={`/automation-runs/${encodeURIComponent(email.automation_run_id)}`}
                prefetch={false}
                aria-label={`Automation run ${email.automation_run_id}`}
              >
                <code>{email.automation_run_id}</code>
              </Link>
            ) : email.automation_id ? (
              <>
                <Link
                  href={`/automations/${encodeURIComponent(email.automation_id)}`}
                  prefetch={false}
                  aria-label={`Automation ${email.automation_id}`}
                >
                  automation
                </Link>{" "}
                <span className="muted">(run deleted)</span>
              </>
            ) : (
              <span className="muted">none</span>
            )}
          </dd>
          <dt>Planned for</dt>
          <dd>
            {email.scheduled_at && email.status === "canceled" ? (
              // No "in 59 minutes": a canceled email is not coming.
              <>
                <time dateTime={email.scheduled_at}>
                  {formatUTC(email.scheduled_at)}
                </time>{" "}
                <span className="muted">(canceled)</span>
              </>
            ) : email.scheduled_at ? (
              <Timestamp iso={email.scheduled_at} />
            ) : (
              <span className="muted">not planned (sent when accepted)</span>
            )}
          </dd>
          <dt>Created</dt>
          <dd>
            <Timestamp iso={email.created_at} />
          </dd>
          <dt>Updated</dt>
          <dd>
            <Timestamp iso={email.updated_at} />
          </dd>
          <dt>Finished</dt>
          <dd>
            <Timestamp iso={email.finished_at} />
          </dd>
        </dl>
      </section>

      <section>
        <h2 id="events-h">Events</h2>
        {events.rows.length === 0 ? (
          <p className="empty">No events recorded yet.</p>
        ) : (
          <div
            className="table-wrap"
            role="region"
            aria-labelledby="events-h"
            tabIndex={0}
          >
            <table>
              <caption className="visually-hidden">
                Events in the order the platform received them
              </caption>
              <thead>
                <tr>
                  <th scope="col">Type</th>
                  <th scope="col">Event time</th>
                  <th scope="col">Received</th>
                  <th scope="col">Provider</th>
                  <th scope="col">Origin</th>
                  <th scope="col">Details</th>
                </tr>
              </thead>
              <tbody>
                {events.rows.map((ev) => (
                  <tr key={ev.id}>
                    <td>{ev.type}</td>
                    <td>
                      <Timestamp iso={ev.occurred_at} />
                    </td>
                    <td>
                      <Timestamp iso={ev.received_at} />
                    </td>
                    <td>{ev.provider ?? <None />}</td>
                    <td>{ev.origin}</td>
                    <td>
                      <Details details={ev.details} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {events.more ? <MoreRows /> : null}
      </section>

      <section>
        <h2 id="deliveries-h">Webhook deliveries</h2>
        {deliveries.rows.length === 0 ? (
          <p className="empty">No webhook deliveries for this email.</p>
        ) : (
          <div
            className="table-wrap"
            role="region"
            aria-labelledby="deliveries-h"
            tabIndex={0}
          >
            <table>
              <caption className="visually-hidden">
                Webhook deliveries, oldest first
              </caption>
              <thead>
                <tr>
                  <th scope="col">Endpoint</th>
                  <th scope="col">Event</th>
                  <th scope="col">Status</th>
                  <th scope="col">Last attempt</th>
                  <th scope="col">Delivered / next attempt</th>
                  <th scope="col">Updated</th>
                </tr>
              </thead>
              <tbody>
                {deliveries.rows.map((d) => (
                  <tr key={d.id}>
                    <td className="wrap url">{d.endpoint_url}</td>
                    <td>{d.event_type}</td>
                    <td>
                      <Status value={d.status} />
                    </td>
                    <td>
                      <LastAttempt delivery={d} />
                    </td>
                    <td>
                      {/* The status says which: delivered, or pending with
                          its next attempt. */}
                      <Timestamp iso={d.delivered_at ?? d.next_attempt_at} />
                    </td>
                    <td>
                      <Timestamp iso={d.updated_at} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {deliveries.more ? <MoreRows /> : null}
      </section>
    </>
  );
}

/** Attempts so far, and what the last one got back. */
function LastAttempt({ delivery: d }: { delivery: WebhookDelivery }) {
  const result = [
    d.last_status_code === null ? null : `HTTP ${String(d.last_status_code)}`,
    d.last_error_code,
  ]
    .filter((part) => part !== null)
    .join(" · ");
  return (
    <span className="pair">
      <span>
        {d.attempts} {d.attempts === 1 ? "attempt" : "attempts"}
      </span>
      {result ? <span className="muted">{result}</span> : null}
    </span>
  );
}

function MoreRows() {
  return (
    <p className="notice">
      Only the first {MAX_ROWS} rows are shown; more exist.
    </p>
  );
}

function Details({ details }: { details: Record<string, string> }) {
  const entries = Object.entries(details);
  if (entries.length === 0) {
    return <None />;
  }
  return (
    <dl className="kv">
      {entries.map(([k, v]) => (
        <div key={k}>
          <dt>{k}</dt>
          <dd className="wrap">{v}</dd>
        </div>
      ))}
    </dl>
  );
}
