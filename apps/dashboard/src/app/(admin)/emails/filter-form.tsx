import Form from "next/form";
import Link from "next/link";
import { DateField, DateRange } from "@/components/date-field";
import type { Project } from "@/lib/admin-api";
import {
  EMAIL_STATUSES,
  FAILURE_CODES,
  emailsHref,
  queryString,
  type EmailFilters,
  type FilterError,
  type FilterField,
  type SearchParams,
} from "@/lib/email-filters";

/**
 * The filter form. It submits with GET to /emails, so every filter ends up in
 * the URL. Fields show what the URL holds, also invalid values, so they can
 * be corrected.
 */
export function FilterForm({
  params,
  filters,
  errors,
  projects,
  moreProjects,
  projectCap,
}: {
  params: SearchParams;
  filters: EmailFilters;
  errors: FilterError[];
  projects: Project[];
  /** The project list was cut; some projects are not offered. */
  moreProjects: boolean;
  projectCap: number;
}) {
  const raw = (field: string) => {
    const v = params[field];
    return typeof v === "string" ? v : "";
  };
  const fieldHints: Partial<Record<FilterField, string>> = {
    ...hints,
    ...(moreProjects
      ? { project_id: `Only the first ${projectCap} projects are listed.` }
      : {}),
  };
  const errorOf = (field: FilterField) => errors.find((e) => e.field === field);
  const fieldProps = (field: FilterField) => {
    const err = errorOf(field);
    return {
      id: `f-${field}`,
      name: field,
      "aria-invalid": err ? true : undefined,
      "aria-describedby":
        [fieldHints[field] ? `h-${field}` : "", err ? `e-${field}` : ""]
          .filter(Boolean)
          .join(" ") || undefined,
    };
  };
  const fieldError = (field: FilterField) => {
    const err = errorOf(field);
    return err ? (
      <p id={`e-${field}`} className="error">
        {err.message}
      </p>
    ) : null;
  };

  // The raw query without the page cursor: invalid values count too, so the
  // fields also follow the URL when only an invalid value changed.
  const formKey = queryString(params, ["cursor"]);
  const templateError = errorOf("template_id");
  const broadcastError = errorOf("broadcast_id");
  const project = raw("project_id").toLowerCase();
  const knownProject = !project || projects.some((p) => p.id === project);
  const statuses = new Set(
    [params["status"] ?? []].flat().filter((s) => s !== ""),
  );

  return (
    // Remounted when the filters change, so the fields follow the URL also
    // on back/forward and nav links.
    <Form
      action="/emails"
      key={formKey}
      className="filters"
      role="search"
      aria-label="Filter emails"
    >
      <div className="field wide">
        <label htmlFor="f-q">Search</label>
        <input
          type="search"
          maxLength={512}
          defaultValue={raw("q")}
          {...fieldProps("q")}
        />
        <p id="h-q" className="hint">
          {hints.q}
        </p>
        {fieldError("q")}
      </div>
      <div className="field">
        <label htmlFor="f-project_id">Project</label>
        <select defaultValue={project} {...fieldProps("project_id")}>
          <option value="">All projects</option>
          {projects.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
              {p.status === "disabled" ? " (disabled)" : ""}
            </option>
          ))}
          {knownProject ? null : (
            <option value={project}>Unknown project</option>
          )}
        </select>
        {fieldHints.project_id ? (
          <p id="h-project_id" className="hint">
            {fieldHints.project_id}
          </p>
        ) : null}
        {fieldError("project_id")}
      </div>
      <div className="field">
        <label htmlFor="f-recipient">Recipient</label>
        <input
          type="text"
          inputMode="email"
          autoComplete="off"
          spellCheck={false}
          maxLength={254}
          defaultValue={raw("recipient")}
          {...fieldProps("recipient")}
        />
        <p id="h-recipient" className="hint">
          {hints.recipient}
        </p>
        {fieldError("recipient")}
      </div>
      <div className="field">
        <label htmlFor="f-error_code">Error code</label>
        <input
          list="error-codes"
          autoComplete="off"
          spellCheck={false}
          maxLength={40}
          defaultValue={raw("error_code")}
          {...fieldProps("error_code")}
        />
        <datalist id="error-codes">
          {FAILURE_CODES.map((c) => (
            <option key={c} value={c} />
          ))}
        </datalist>
        {fieldError("error_code")}
      </div>
      {/* From and to share a row, so the range reads as one. */}
      <div className="wide date-range">
        <DateRange from={raw("from")} to={raw("to")}>
          <div className="field">
            <label htmlFor="f-from">From date (UTC)</label>
            <DateField
              label="from date"
              range="from"
              defaultValue={raw("from")}
              disableFuture
              {...fieldProps("from")}
            />
            <p id="h-from" className="hint">
              {hints.from}
            </p>
            {fieldError("from")}
          </div>
          <div className="field">
            <label htmlFor="f-to">To date (UTC, inclusive)</label>
            <DateField
              label="to date"
              range="to"
              defaultValue={raw("to")}
              disableFuture
              {...fieldProps("to")}
            />
            <p id="h-to" className="hint">
              {hints.to}
            </p>
            {fieldError("to")}
          </div>
        </DateRange>
      </div>
      <fieldset
        className="field wide statuses"
        aria-describedby={errorOf("status") ? "e-status" : undefined}
      >
        <legend>Status</legend>
        <ul>
          {EMAIL_STATUSES.map((s, i) => (
            <li key={s}>
              <label>
                <input
                  type="checkbox"
                  id={i === 0 ? "f-status" : undefined}
                  name="status"
                  value={s}
                  defaultChecked={statuses.has(s)}
                />{" "}
                {s}
              </label>
            </li>
          ))}
        </ul>
        {fieldError("status")}
      </fieldset>
      {filters.template_id ? (
        <p className="wide">
          <input type="hidden" name="template_id" value={filters.template_id} />
          Only emails of template <code>{filters.template_id}</code>.{" "}
          <Link href={emailsHref({ ...filters, template_id: undefined })}>
            Remove template filter
          </Link>
        </p>
      ) : null}
      {templateError ? (
        <p className="wide error">{templateError.message}</p>
      ) : null}
      {filters.broadcast_id ? (
        <p className="wide">
          <input
            type="hidden"
            name="broadcast_id"
            value={filters.broadcast_id}
          />
          Only recipient emails of broadcast{" "}
          <Link
            href={`/broadcasts/${encodeURIComponent(filters.broadcast_id)}`}
            prefetch={false}
            aria-label={`Broadcast ${filters.broadcast_id}`}
          >
            <code>{filters.broadcast_id}</code>
          </Link>
          .{" "}
          <Link href={emailsHref({ ...filters, broadcast_id: undefined })}>
            Remove broadcast filter
          </Link>
        </p>
      ) : null}
      {broadcastError ? (
        <p className="wide error">{broadcastError.message}</p>
      ) : null}
      <div className="actions wide">
        <button type="submit" className="primary">
          Apply filters
        </button>
        <Link href="/emails" className="button">
          Clear filters
        </Link>
      </div>
    </Form>
  );
}

const hints: Partial<Record<FilterField, string>> = {
  q: "Email id, provider message id or exact recipient address.",
  recipient: "Exact address, not case-sensitive.",
  from: "YYYY-MM-DD",
  to: "YYYY-MM-DD",
};
