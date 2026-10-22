/** The name as shown; a blank one would leave a link without a name. */
export function broadcastName(name: string): string {
  return name.trim() === "" ? "(no name)" : name;
}

/** Recipient email statuses in the order they are shown. */
export const RECIPIENT_STATUSES = [
  "queued",
  "sending",
  "captured",
  "submitted",
  "delivered",
  "bounced",
  "complained",
  "failed",
  "canceled",
] as const;
