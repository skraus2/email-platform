import type { Metadata } from "next";
import Link from "next/link";
import { unstable_rethrow } from "next/navigation";
import { Suspense } from "react";
import { Status } from "@/components/status";
import { Timestamp } from "@/components/timestamp";
import {
  AdminAPIError,
  adminFetch,
  adminFetchAll,
  type AdminEmail,
  type Page,
  type Project,
} from "@/lib/admin-api";
import {
  apiQuery,
  emailsHref,
  hasFilters,
  isFailuresView,
  parseEmailFilters,
  type EmailFilters,
} from "@/lib/email-filters";
import { FilterForm } from "./filter-form";

export async function generateMetadata({
  searchParams,
}: PageProps<"/emails">): Promise<Metadata> {
  const { filters } = parseEmailFilters(await searchParams);
  return { title: isFailuresView(filters) ? "Failures" : "Messages" };
}

export default async function EmailsPage({
  searchParams,
}: PageProps<"/emails">) {
  const params = await searchParams;
  const { filters, errors } = parseEmailFilters(params);
  // Also the session check of this page (see adminFetch).
  const projects = await adminFetchAll<Project>("/admin/v1/projects");
  const failures = isFailuresView(filters);

  return (
    <>
      <h1>{failures ? "Failures" : "Messages"}</h1>
      <p className="muted">
        {failures
          ? "Emails that could not be sent, newest first."
          : "Emails of all projects, newest first. Times are in UTC."}
      </p>
      <FilterForm
        params={params}
        filters={filters}
        errors={errors}
        projects={projects.rows}
      />
      {errors.length > 0 ? (
        <div className="notice" role="alert">
          <p>The filters could not be applied:</p>
          <ul>
            {errors.map((e) => (
              <li key={e.field}>
                {e.field === "cursor" || e.field === "template_id" ? (
                  e.message
                ) : (
                  <a href={`#f-${e.field}`}>{e.message}</a>
                )}
              </li>
            ))}
          </ul>
        </div>
      ) : (
        // Keyed by the query, so a new search shows the loading state.
        <Suspense
          key={emailsHref(filters, filters.cursor)}
          fallback={
            <p role="status" className="loading">
              Loading emails…
            </p>
          }
        >
          <EmailResults filters={filters} />
        </Suspense>
      )}
    </>
  );
}

async function EmailResults({ filters }: { filters: EmailFilters }) {
  let page: Page<AdminEmail>;
  try {
    page = await adminFetch<Page<AdminEmail>>("/admin/v1/emails", {
      query: apiQuery(filters),
    });
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof AdminAPIError && err.status === 400) {
      return (
        <div className="notice" role="alert">
          {err.code === "invalid_cursor" ? (
            <p>
              This page link is no longer valid.{" "}
              <Link href={emailsHref(filters)}>Go to the first page</Link>.
            </p>
          ) : (
            <p>The filters could not be applied: {err.message}</p>
          )}
        </div>
      );
    }
    throw err;
  }

  const firstPage = filters.cursor ? emailsHref(filters) : undefined;
  if (page.data.length === 0) {
    return (
      <div className="empty" role="status">
        {firstPage ? (
          <p>
            No more emails. <Link href={firstPage}>Go to the first page</Link>.
          </p>
        ) : hasFilters(filters) ? (
          <p>
            No emails match these filters.{" "}
            <Link href="/emails">Clear filters</Link>
          </p>
        ) : (
          <p>No emails yet.</p>
        )}
      </div>
    );
  }

  return (
    <>
      <div
        className="table-wrap"
        role="region"
        aria-labelledby="emails-caption"
        tabIndex={0}
      >
        <table>
          <caption id="emails-caption">
            {page.data.length} email{page.data.length === 1 ? "" : "s"}
            {filters.cursor ? " (later page)" : ""}
          </caption>
          <thead>
            <tr>
              <th scope="col">Created</th>
              <th scope="col">Project</th>
              <th scope="col">Recipient</th>
              <th scope="col">Subject</th>
              <th scope="col">Status</th>
              <th scope="col">Error code</th>
              <th scope="col">Template</th>
            </tr>
          </thead>
          <tbody>
            {page.data.map((e) => (
              <tr key={e.id}>
                <td>
                  <Timestamp iso={e.created_at} />
                </td>
                <td>{e.project_name}</td>
                <td className="wrap">{e.to}</td>
                <td className="wrap">
                  <Link href={`/emails/${encodeURIComponent(e.id)}`}>
                    {e.subject}
                  </Link>
                </td>
                <td>
                  <Status value={e.status} />
                </td>
                <td>{e.error_code ?? <span className="muted">—</span>}</td>
                <td>
                  {e.template_id ? (
                    <span title={e.template_id}>
                      <code>{e.template_id.slice(0, 8)}…</code> v
                      {e.template_version}
                    </span>
                  ) : (
                    <span className="muted">—</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {firstPage || page.next_cursor ? (
        <nav aria-label="Pages" className="pager">
          <ul>
            {firstPage ? (
              <li>
                <Link href={firstPage}>First page</Link>
              </li>
            ) : null}
            {page.next_cursor ? (
              <li>
                <Link href={emailsHref(filters, page.next_cursor)}>
                  Next page
                </Link>
              </li>
            ) : null}
          </ul>
        </nav>
      ) : null}
    </>
  );
}
