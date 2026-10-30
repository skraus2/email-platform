import { BackLink } from "@/components/back-link";

export default function TemplateNotFound() {
  return (
    <>
      <BackLink href="/templates">All templates</BackLink>
      <h1>Template not found</h1>
      <p className="notice">There is no template with this id.</p>
    </>
  );
}
