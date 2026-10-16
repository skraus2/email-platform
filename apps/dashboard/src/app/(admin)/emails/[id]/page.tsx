import type { Metadata } from "next";
import Link from "next/link";
import { notFound, unstable_rethrow } from "next/navigation";
import { Status } from "@/components/status";
import { Timestamp } from "@/components/timestamp";
import {
  AdminAPIError,
  adminFetch,
  adminFetchAll,
  type AdminEmail,
  type EmailEvent,
  type WebhookDelivery,
} from "@/lib/admin-api";
import { isUUID } from "@/lib/email-filters";

export const metadata: Metadata = { title: "Email details" };

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
      adminFetch<AdminEmail>(base),
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
      <p>
        <Link href="/emails">All messages</Link>
      </p>
      <h1 className="wrap">{email.subject}</h1>
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
          <dd>{email.error_code ?? <Missing />}</dd>
          <dt>Project</dt>
          <dd>
            {email.project_name}{" "}
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
          <dd>{email.provider ?? <Missing />}</dd>
          <dt>Provider message id</dt>
          <dd className="wrap">
            {email.provider_id ? <code>{email.provider_id}</code> : <Missing />}
          </dd>
          <dt>Attempts</dt>
          <dd>{email.attempts}</dd>
          <dt>Template</dt>
          <dd>
            {email.template_id ? (
              <>
                <code>{email.template_id}</code>, version{" "}
                {email.template_version}
              </>
            ) : (
              <span className="muted">none (raw content)</span>
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

      <section aria-labelledby="events-h">
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
                    <td>{ev.provider ?? <Missing />}</td>
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

      <section aria-labelledby="deliveries-h">
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
                  <th scope="col">Attempts</th>
                  <th scope="col">Last HTTP status</th>
                  <th scope="col">Last error code</th>
                  <th scope="col">Next attempt</th>
                  <th scope="col">Delivered</th>
                  <th scope="col">Updated</th>
                </tr>
              </thead>
              <tbody>
                {deliveries.rows.map((d) => (
                  <tr key={d.id}>
                    <td className="wrap">{d.endpoint_url}</td>
                    <td>{d.event_type}</td>
                    <td>
                      <Status value={d.status} />
                    </td>
                    <td>{d.attempts}</td>
                    <td>{d.last_status_code ?? <Missing />}</td>
                    <td>{d.last_error_code ?? <Missing />}</td>
                    <td>
                      <Timestamp iso={d.next_attempt_at} />
                    </td>
                    <td>
                      <Timestamp iso={d.delivered_at} />
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

function Missing() {
  return <span className="muted">—</span>;
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
    return <Missing />;
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
