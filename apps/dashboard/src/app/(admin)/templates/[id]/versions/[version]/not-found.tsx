import { BackLink } from "@/components/back-link";

export default function VersionNotFound() {
  return (
    <>
      <BackLink href="/templates">All templates</BackLink>
      <h1>Template version not found</h1>
      <p className="notice">
        There is no template version with this id and number.
      </p>
    </>
  );
}
