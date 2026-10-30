import {
  Skeleton,
  SkeletonBack,
  SkeletonCard,
  SkeletonHeading,
  SkeletonTable,
  SkeletonTitle,
} from "@/components/skeleton";

export default function Loading() {
  return (
    <Skeleton label="Loading run…">
      <SkeletonBack />
      <SkeletonTitle />
      <SkeletonHeading />
      <SkeletonCard rows={6} />
      <SkeletonHeading />
      <SkeletonTable rows={2} cols={3} />
    </Skeleton>
  );
}
