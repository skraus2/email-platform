import Link from "next/link";

export default function EmailNotFound() {
  return (
    <>
      <h1>Email not found</h1>
      <p>There is no email with this id.</p>
      <Link href="/emails">All messages</Link>
    </>
  );
}
