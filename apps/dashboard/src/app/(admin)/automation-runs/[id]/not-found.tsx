import Link from "next/link";

export default function RunNotFound() {
  return (
    <>
      <h1>Run not found</h1>
      <p>There is no automation run with this id.</p>
      <Link href="/automations">All automations</Link>
    </>
  );
}
