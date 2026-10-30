import {
  Skeleton,
  SkeletonFilters,
  SkeletonHeading,
  SkeletonTable,
  SkeletonTitle,
} from "@/components/skeleton";

export default function Loading() {
  return (
    <Skeleton label="Loading messages…">
      <SkeletonTitle />
      <SkeletonFilters />
      <SkeletonHeading />
      <SkeletonTable rows={8} cols={7} />
    </Skeleton>
  );
}
