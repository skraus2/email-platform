import type { Metadata } from "next";
import Link from "next/link";
import { unstable_rethrow } from "next/navigation";
import { Suspense } from "react";
import { None } from "@/components/none";
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
  queryString,
  type EmailFilters,
} from "@/lib/email-filters";
import { displaySubject } from "@/lib/format";
import { FilterForm } from "./filter-form";
import { ResultsReady, ResultsScope } from "./results-ready";

/** Projects offered in the project filter. */
const PROJECT_CAP = 500;

export async function generateMetadata({
  searchParams,
}: PageProps<"/emails">): Promise<Metadata> {
  const { filters, errors } = parseEmailFilters(await searchParams);
  const view = isFailuresView(filters) ? "Failures" : "Messages";
  return { title: errors.length > 0 ? `Error: ${view}` : view };
}

export default async function EmailsPage({
  searchParams,
}: PageProps<"/emails">) {
  const params = await searchParams;
  const { filters, errors } = parseEmailFilters(params);
  // Also the session check of this page (see adminFetch).
  const projects = await adminFetchAll<Project>(
    "/admin/v1/projects",
    PROJECT_CAP,
  );
  const query = queryString(params);
  const failures = isFailuresView(filters);

  return (
    <ResultsScope query={query}>
      <h1>{failures ? "Failures" : "Messages"}</h1>
      <p className="muted">
        {failures
          ? "Emails that could not be sent, newest first."
          : "Emails of all projects, newest first."}{" "}
        Times are in UTC.
      </p>
      {errors.length > 0 ? (
        <div id="filter-errors" className="notice" tabIndex={-1}>
          <p>The filters could not be applied:</p>
          <ul>
            {errors.map((e) => (
              <li key={e.field}>
                {e.field === "cursor" ? (
                  <>
                    {e.message}{" "}
                    <Link href={emailsHref(filters)}>Go to the first page</Link>
                    .
                  </>
                ) : e.field === "template_id" ? (
                  e.message
                ) : (
                  <a href={`#f-${e.field}`}>{e.message}</a>
                )}
              </li>
            ))}
          </ul>
          <ResultsReady query={query} focusId="filter-errors" />
        </div>
      ) : null}
      <FilterForm
        params={params}
        filters={filters}
        errors={errors}
        projects={projects.rows}
        moreProjects={projects.more}
        projectCap={PROJECT_CAP}
      />
      {/* Persistent, so result changes are announced reliably. */}
      <p id="results-status" role="status" className="visually-hidden" />
      {errors.length > 0 ? null : (
        <section aria-labelledby="results-h">
          <h2 id="results-h" tabIndex={-1}>
            Results
          </h2>
          {/* Keyed by the query, so a new search shows the loading state. */}
          <Suspense
            key={emailsHref(filters, filters.cursor)}
            fallback={<p className="loading">Loading emails…</p>}
          >
            <EmailResults filters={filters} query={query} />
          </Suspense>
        </section>
      )}
    </ResultsScope>
  );
}

async function EmailResults({
  filters,
  query,
}: {
  filters: EmailFilters;
  query: string;
}) {
  let page: Page<AdminEmail>;
  try {
    page = await adminFetch<Page<AdminEmail>>("/admin/v1/emails", {
      query: apiQuery(filters),
    });
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof AdminAPIError && err.status === 400) {
      const message =
        err.code === "invalid_cursor"
          ? "This page link is no longer valid."
          : `The filters could not be applied: ${err.message}`;
      return (
        <div className="notice">
          <p>
            {message}
            {err.code === "invalid_cursor" ? (
              <>
                {" "}
                <Link href={emailsHref(filters)}>Go to the first page</Link>.
              </>
            ) : null}
          </p>
          <ResultsReady message={message} query={query} focusId="results-h" />
        </div>
      );
    }
    throw err;
  }

  const firstPage = filters.cursor ? emailsHref(filters) : undefined;
  if (page.data.length === 0) {
    const message = firstPage
      ? "No more emails."
      : hasFilters(filters)
        ? "No emails match these filters."
        : "No emails yet.";
    return (
      <div className="empty">
        <p>
          {message}{" "}
          {firstPage ? (
            <>
              <Link href={firstPage}>Go to the first page</Link>.
            </>
          ) : hasFilters(filters) ? (
            <Link href="/emails">Clear filters</Link>
          ) : null}
        </p>
        <ResultsReady message={message} query={query} focusId="results-h" />
      </div>
    );
  }

  const count = `${page.data.length} email${page.data.length === 1 ? "" : "s"}`;
  const summary = `${count}${filters.cursor ? " on this later page" : ""}, newest first${page.next_cursor ? "; more on the next page" : ""}`;
  return (
    <>
      <ResultsReady message={`${summary}.`} query={query} focusId="results-h" />
      <div
        className="table-wrap"
        role="region"
        aria-labelledby="emails-caption"
        tabIndex={0}
      >
        <table>
          <caption id="emails-caption">{summary}</caption>
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
                  {/* No prefetch: it would load every listed email's details
                      (and its title) from the admin API. */}
                  <Link
                    href={`/emails/${encodeURIComponent(e.id)}`}
                    prefetch={false}
                  >
                    {displaySubject(e.subject)}
                  </Link>
                </td>
                <td>
                  <Status value={e.status} />
                </td>
                <td>{e.error_code ?? <None />}</td>
                <td>
                  {e.template_id ? (
                    <Link
                      href={`/templates/${encodeURIComponent(e.template_id)}/versions/${e.template_version}`}
                      prefetch={false}
                    >
                      <span className="visually-hidden">Template </span>
                      <code>{e.template_id.slice(0, 8)}…</code> version{" "}
                      {e.template_version}
                    </Link>
                  ) : (
                    <None />
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
