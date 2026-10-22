import type { Metadata } from "next";
import Link from "next/link";
import { Status } from "@/components/status";
import { Timestamp } from "@/components/timestamp";
import { adminFetchAll, type Broadcast } from "@/lib/admin-api";
import { broadcastName } from "@/lib/broadcasts";

export const metadata: Metadata = { title: "Broadcasts" };

/** Broadcasts loaded for this view. */
const BROADCAST_CAP = 500;

export default async function BroadcastsPage() {
  // Also the session check of this page (see adminFetch).
  const broadcasts = await adminFetchAll<Broadcast>(
    "/admin/v1/broadcasts",
    BROADCAST_CAP,
  );

  return (
    <>
      <h1>Broadcasts</h1>
      <p className="muted">
        Broadcasts of all projects, newest first. A completed broadcast has
        created all its recipient emails; their delivery is shown per email.
        Times are in UTC.
      </p>
      {broadcasts.more ? (
        <p className="notice">
          Only the first {BROADCAST_CAP} broadcasts are shown; more exist.
        </p>
      ) : null}
      {broadcasts.rows.length === 0 ? (
        <p className="empty">No broadcasts yet.</p>
      ) : (
        <div
          className="table-wrap"
          role="region"
          aria-labelledby="broadcasts-caption"
          tabIndex={0}
        >
          <table>
            <caption id="broadcasts-caption" className="visually-hidden">
              Broadcasts, newest first
            </caption>
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">Project</th>
                <th scope="col">Status</th>
                <th scope="col">Starts</th>
                <th scope="col">Created</th>
              </tr>
            </thead>
            <tbody>
              {broadcasts.rows.map((b) => (
                <tr key={b.id}>
                  <td className="wrap">
                    <Link
                      href={`/broadcasts/${encodeURIComponent(b.id)}`}
                      prefetch={false}
                    >
                      {broadcastName(b.name)}
                    </Link>
                  </td>
                  <td className="wrap">{b.project_name}</td>
                  <td>
                    <Status value={b.status} />
                  </td>
                  <td>
                    <Timestamp iso={b.scheduled_at} />
                  </td>
                  <td>
                    <Timestamp iso={b.created_at} />
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
