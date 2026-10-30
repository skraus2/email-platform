import { BackLink } from "@/components/back-link";

export default function EmailNotFound() {
  return (
    <>
      <BackLink href="/emails">All messages</BackLink>
      <h1>Email not found</h1>
      <p className="notice">There is no email with this id.</p>
    </>
  );
}
