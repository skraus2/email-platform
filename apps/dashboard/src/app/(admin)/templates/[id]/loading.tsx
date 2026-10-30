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
    <Skeleton label="Loading template…">
      <SkeletonBack />
      <SkeletonTitle />
      <SkeletonHeading />
      <SkeletonCard rows={5} />
      <SkeletonHeading />
      <SkeletonTable rows={3} cols={2} />
    </Skeleton>
  );
}
