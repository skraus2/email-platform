import { BackLink } from "@/components/back-link";

export default function ProjectNotFound() {
  return (
    <>
      <BackLink href="/projects">All projects</BackLink>
      <h1>Project not found</h1>
      <p className="notice">There is no project with this id.</p>
    </>
  );
}
