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
    <Skeleton label="Loading email…">
      <SkeletonBack />
      <SkeletonTitle />
      <SkeletonHeading />
      <SkeletonCard rows={16} />
      <SkeletonHeading />
      <SkeletonTable rows={3} cols={6} />
      <SkeletonHeading />
      <SkeletonTable rows={2} cols={6} />
    </Skeleton>
  );
}
