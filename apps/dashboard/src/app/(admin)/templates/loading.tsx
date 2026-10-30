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
      <SkeletonHeading />
      <SkeletonTable rows={2} cols={4} />
      <SkeletonHeading />
      <SkeletonTable rows={2} cols={4} />
    </Skeleton>
  );
}
