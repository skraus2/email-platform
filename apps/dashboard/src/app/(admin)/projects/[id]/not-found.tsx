import Link from "next/link";

export default function ProjectNotFound() {
  return (
    <>
      <h1>Project not found</h1>
      <p>There is no project with this id.</p>
      <Link href="/projects">All projects</Link>
    </>
  );
}
