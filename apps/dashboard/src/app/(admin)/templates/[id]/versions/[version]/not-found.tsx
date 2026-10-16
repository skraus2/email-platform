import Link from "next/link";

export default function VersionNotFound() {
  return (
    <>
      <h1>Template version not found</h1>
      <p>There is no template version with this id and number.</p>
      <Link href="/templates">All templates</Link>
    </>
  );
}
