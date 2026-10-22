const TONE: Record<string, "ok" | "bad" | "wait" | "off"> = {
  captured: "ok",
  submitted: "ok",
  delivered: "ok",
  active: "ok",
  verified: "ok",
  bounced: "bad",
  complained: "bad",
  failed: "bad",
  disabled: "bad",
  queued: "wait",
  sending: "wait",
  pending: "wait",
  draft: "wait",
  scheduled: "wait",
  completed: "ok",
  canceled: "off",
};

/** A status as text; the colour is only an extra cue. */
export function Status({ value }: { value: string }) {
  return (
    <span className={`status status-${TONE[value] ?? "wait"}`}>{value}</span>
  );
}
