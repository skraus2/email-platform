import Link from "next/link";

export default function BroadcastNotFound() {
  return (
    <>
      <h1>Broadcast not found</h1>
      <p>There is no broadcast with this id.</p>
      <Link href="/broadcasts">All broadcasts</Link>
    </>
  );
}
