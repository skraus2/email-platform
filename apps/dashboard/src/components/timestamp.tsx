import { None } from "@/components/none";
import { formatUTC, relativeTime } from "@/lib/format";

/** A UTC timestamp with a relative hint ("3 minutes ago"); "—" when absent. */
export function Timestamp({ iso }: { iso: string | null }) {
  if (!iso) {
    return <None />;
  }
  const hint = relativeTime(iso, new Date());
  return (
    <span className="ts">
      <time dateTime={iso}>{formatUTC(iso)}</time>
      {hint ? <span className="muted">{hint}</span> : null}
    </span>
  );
}
