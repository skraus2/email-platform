import Link from "next/link";
import { None } from "@/components/none";
import { Timestamp } from "@/components/timestamp";
import type { Template } from "@/lib/admin-api";

/** Templates of one project as a table; `labelId` names the table region. */
export function TemplatesTable({
  templates,
  labelId,
  caption,
}: {
  templates: Template[];
  labelId: string;
  caption: string;
}) {
  return (
    <div
      className="table-wrap templates"
      role="region"
      aria-labelledby={labelId}
      tabIndex={0}
    >
      <table>
        <caption className="visually-hidden">{caption}</caption>
        <thead>
          <tr>
            <th scope="col">Name</th>
            <th scope="col">Slug</th>
            <th scope="col">Latest version</th>
            <th scope="col">Created</th>
          </tr>
        </thead>
        <tbody>
          {templates.map((t) => (
            <tr key={t.id}>
              <td className="wrap">
                {/* No prefetch: it would run the detail page's title lookup
                    against the admin API for every listed template. */}
                <Link
                  href={`/templates/${encodeURIComponent(t.id)}`}
                  prefetch={false}
                >
                  {displayName(t.name)}
                </Link>
              </td>
              <td className="wrap">
                <code>{t.slug}</code>
              </td>
              <td>{t.latest_version ?? <None />}</td>
              <td>
                <Timestamp iso={t.created_at} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** The name as shown; a blank one would leave a link without a name. */
export function displayName(name: string): string {
  return name.trim() === "" ? "(no name)" : name;
}
