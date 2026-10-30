import type { Metadata } from "next";
import Link from "next/link";
import { notFound, unstable_rethrow } from "next/navigation";
import { cache } from "react";
import { BackLink } from "@/components/back-link";
import { Status } from "@/components/status";
import { Timestamp } from "@/components/timestamp";
import {
  AdminAPIError,
  adminFetch,
  type BroadcastDetail,
} from "@/lib/admin-api";
import { broadcastName, RECIPIENT_STATUSES } from "@/lib/broadcasts";
import { emailsHref, isUUID } from "@/lib/email-filters";
import { shorten } from "@/lib/format";

/** A count with digit grouping (50,000). */
function count(n: number): string {
  return n.toLocaleString("en");
}

const loadBroadcast = cache((id: string) =>
  adminFetch<BroadcastDetail>(`/admin/v1/broadcasts/${encodeURIComponent(id)}`),
);

export async function generateMetadata({
  params,
}: PageProps<"/broadcasts/[id]">): Promise<Metadata> {
  const { id } = await params;
  if (!isUUID(id)) {
    return { title: "Broadcast not found" };
  }
  try {
    const b = await loadBroadcast(id.toLowerCase());
    return { title: `Broadcast: ${shorten(broadcastName(b.name), 60)}` };
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof AdminAPIError && err.status === 404) {
      return { title: "Broadcast not found" };
    }
    throw err;
  }
}

export default async function BroadcastPage({
  params,
}: PageProps<"/broadcasts/[id]">) {
  const { id } = await params;
  if (!isUUID(id)) {
    notFound();
  }
  let b: BroadcastDetail;
  try {
    b = await loadBroadcast(id.toLowerCase());
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof AdminAPIError && err.status === 404) {
      notFound();
    }
    throw err;
  }
  const c = b.counts;
  const skipped =
    c.skipped_unsubscribed + c.skipped_suppressed + c.skipped_invalid;

  return (
    <>
      <BackLink href="/broadcasts">All broadcasts</BackLink>
      <h1 className="wrap">{broadcastName(b.name)}</h1>
      <p className="muted">
        Broadcast <code>{b.id}</code>
      </p>

      <section aria-labelledby="details-h">
        <h2 id="details-h">Details</h2>
        <dl className="meta">
          <dt>Status</dt>
          <dd>
            <Status value={b.status} />
          </dd>
          <dt>Project</dt>
          <dd className="wrap">
            <Link
              href={`/projects/${encodeURIComponent(b.project_id)}`}
              prefetch={false}
            >
              {b.project_name}
            </Link>
          </dd>
          <dt>From</dt>
          <dd className="wrap">{b.from}</dd>
          <dt>Template</dt>
          <dd>
            <Link
              href={`/templates/${encodeURIComponent(b.template_id)}/versions/${b.template_version}`}
              prefetch={false}
            >
              Version {b.template_version}
            </Link>{" "}
            of template{" "}
            <Link
              href={`/templates/${encodeURIComponent(b.template_id)}`}
              prefetch={false}
            >
              <code>{b.template_id}</code>
            </Link>
          </dd>
          <dt>Audience</dt>
          <dd>
            <code>{b.audience_id}</code>
          </dd>
          <dt>Unsubscribe page</dt>
          <dd className="wrap">
            <code>{b.unsubscribe_url}</code>
          </dd>
          <dt>Planned start</dt>
          <dd>
            <Timestamp iso={b.scheduled_at} />
          </dd>
          <dt>Started</dt>
          <dd>
            <Timestamp iso={b.started_at} />
          </dd>
          <dt>Finished</dt>
          <dd>
            <Timestamp iso={b.finished_at} />
          </dd>
          <dt>Created</dt>
          <dd>
            <Timestamp iso={b.created_at} />
          </dd>
        </dl>
      </section>

      <section aria-labelledby="progress-h">
        <h2 id="progress-h">Progress</h2>
        <dl className="meta">
          <dt>Recipient emails created</dt>
          <dd>{count(c.recipients)}</dd>
          <dt>Members skipped</dt>
          <dd>
            {count(skipped)} ({count(c.skipped_unsubscribed)} unsubscribed,{" "}
            {count(c.skipped_suppressed)} suppressed, {count(c.skipped_invalid)}{" "}
            not renderable)
          </dd>
        </dl>
        {c.recipients === 0 &&
        (b.status === "draft" || b.status === "scheduled") ? (
          <p className="empty">
            No recipient emails yet; they are created when the broadcast starts.
          </p>
        ) : c.recipients === 0 && b.status === "canceled" ? (
          <p className="empty">
            No recipient emails; the broadcast was canceled before it created
            any.
          </p>
        ) : (
          <>
            <div
              className="table-wrap compact"
              role="region"
              aria-labelledby="by-status-caption"
              tabIndex={0}
            >
              <table>
                <caption id="by-status-caption">
                  Recipient emails by status
                </caption>
                <thead>
                  <tr>
                    <th scope="col">Status</th>
                    <th scope="col" className="num">
                      Emails
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {RECIPIENT_STATUSES.map((st) => (
                    <tr key={st}>
                      <th scope="row">
                        <Status value={st} />
                      </th>
                      <td className="num">{count(c.by_status[st] ?? 0)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <p>
              <Link href={emailsHref({ status: [], broadcast_id: b.id })}>
                Show the recipient emails
              </Link>
            </p>
          </>
        )}
      </section>
    </>
  );
}
