import {
  Skeleton,
  SkeletonBack,
  SkeletonBlock,
  SkeletonCard,
  SkeletonHeading,
  SkeletonTable,
  SkeletonTitle,
} from "@/components/skeleton";

export default function Loading() {
  return (
    <Skeleton label="Loading template version…">
      <SkeletonBack />
      <SkeletonTitle intro={false} />
      <SkeletonCard rows={2} />
      <SkeletonHeading />
      <SkeletonTable rows={3} cols={4} />
      <SkeletonHeading />
      <SkeletonBlock size="small" />
      <SkeletonBlock size="medium" />
      <SkeletonBlock size="medium" />
      <SkeletonHeading />
      <SkeletonBlock size="large" />
    </Skeleton>
  );
}
