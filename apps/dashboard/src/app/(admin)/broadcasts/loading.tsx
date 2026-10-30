import { Skeleton, SkeletonTable, SkeletonTitle } from "@/components/skeleton";

export default function Loading() {
  return (
    <Skeleton label="Loading broadcasts…">
      <SkeletonTitle />
      <SkeletonTable rows={5} cols={5} caption />
    </Skeleton>
  );
}
