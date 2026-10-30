import { Skeleton, SkeletonTable, SkeletonTitle } from "@/components/skeleton";

export default function Loading() {
  return (
    <Skeleton label="Loading automations…">
      <SkeletonTitle />
      <SkeletonTable rows={4} cols={6} />
    </Skeleton>
  );
}
