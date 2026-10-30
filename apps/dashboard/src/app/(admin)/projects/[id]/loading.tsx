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
    <Skeleton label="Loading project…">
      <SkeletonBack />
      <SkeletonTitle />
      <SkeletonHeading />
      <SkeletonCard rows={3} />
      <SkeletonHeading />
      <SkeletonTable rows={2} cols={5} />
      <SkeletonHeading />
      <SkeletonTable rows={2} cols={5} />
      <SkeletonHeading />
      <SkeletonTable rows={2} cols={4} />
    </Skeleton>
  );
}
