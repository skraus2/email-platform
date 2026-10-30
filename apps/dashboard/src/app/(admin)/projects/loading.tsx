import { Skeleton, SkeletonTable, SkeletonTitle } from "@/components/skeleton";

export default function Loading() {
  return (
    <Skeleton label="Loading projects…">
      <SkeletonTitle />
      <SkeletonTable rows={5} cols={4} caption />
    </Skeleton>
  );
}
