import { BackLink } from "@/components/back-link";

export default function RunNotFound() {
  return (
    <>
      <BackLink href="/automations">All automations</BackLink>
      <h1>Run not found</h1>
      <p className="notice">There is no automation run with this id.</p>
    </>
  );
}
