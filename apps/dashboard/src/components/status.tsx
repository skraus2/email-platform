const TONE: Record<string, "ok" | "bad" | "wait"> = {
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
};

/** A status as text; the colour is only an extra cue. */
export function Status({ value }: { value: string }) {
  return (
    <span className={`status status-${TONE[value] ?? "wait"}`}>{value}</span>
  );
}
