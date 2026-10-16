import Link from "next/link";

export default function TemplateNotFound() {
  return (
    <>
      <h1>Template not found</h1>
      <p>There is no template with this id.</p>
      <Link href="/templates">All templates</Link>
    </>
  );
}
