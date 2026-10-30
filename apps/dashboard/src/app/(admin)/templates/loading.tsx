import {
  Skeleton,
  SkeletonHeading,
  SkeletonTable,
  SkeletonTitle,
} from "@/components/skeleton";

export default function Loading() {
  return (
    <Skeleton label="Loading templates…">
      <SkeletonTitle />
      {/* One project group: how many there are depends on the data. */}
      <SkeletonHeading />
      <SkeletonTable rows={3} cols={4} />
    </Skeleton>
  );
}
