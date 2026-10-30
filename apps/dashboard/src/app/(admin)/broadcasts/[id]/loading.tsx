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
    <Skeleton label="Loading broadcast…">
      <SkeletonBack />
      <SkeletonTitle />
      <SkeletonHeading />
      <SkeletonCard rows={10} />
      <SkeletonHeading />
      <SkeletonCard rows={2} />
      <SkeletonTable rows={9} cols={2} compact caption />
    </Skeleton>
  );
}
