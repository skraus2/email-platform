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
    <Skeleton label="Loading automation…">
      <SkeletonBack />
      <SkeletonTitle />
      <SkeletonHeading />
      <SkeletonCard rows={6} />
      <SkeletonHeading />
      <SkeletonCard rows={4} />
      <SkeletonHeading />
      <SkeletonTable rows={5} cols={2} compact />
      <SkeletonHeading />
      <SkeletonTable rows={5} cols={5} />
    </Skeleton>
  );
}
