import Link from "next/link";
import type { ReactNode } from "react";
import { Icon } from "@/components/icons";

/** The link up to the list or parent page, above a detail page's title. */
export function BackLink({
  href,
  prefetch = null,
  children,
}: {
  href: string;
  /** As on Link; left out, Next.js decides. */
  prefetch?: boolean | null;
  children: ReactNode;
}) {
  return (
    <p className="back">
      <Link href={href} prefetch={prefetch}>
        <Icon name="chevronLeft" />
        <span>{children}</span>
      </Link>
    </p>
  );
}
