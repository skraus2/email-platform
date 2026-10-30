import { BackLink } from "@/components/back-link";

export default function AutomationNotFound() {
  return (
    <>
      <BackLink href="/automations">All automations</BackLink>
      <h1>Automation not found</h1>
      <p className="notice">There is no automation with this id.</p>
    </>
  );
}
