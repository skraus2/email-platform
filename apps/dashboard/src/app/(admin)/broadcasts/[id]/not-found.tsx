import { BackLink } from "@/components/back-link";

export default function BroadcastNotFound() {
  return (
    <>
      <BackLink href="/broadcasts">All broadcasts</BackLink>
      <h1>Broadcast not found</h1>
      <p className="notice">There is no broadcast with this id.</p>
    </>
  );
}
