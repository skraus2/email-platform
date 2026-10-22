import Link from "next/link";

export default function AutomationNotFound() {
  return (
    <>
      <h1>Automation not found</h1>
      <p>There is no automation with this id.</p>
      <Link href="/automations">All automations</Link>
    </>
  );
}
