import Link from "next/link";
import { Icon } from "@/components/icons";

/**
 * The product name with its mark. Links to the overview, except where that
 * would lead nowhere (the sign-in page, before there is a session).
 */
export function Brand({ linked = true }: { linked?: boolean }) {
  const content = (
    <>
      <span className="brand-mark">
        <Icon name="messages" />
      </span>
      Email Platform
    </>
  );
  return linked ? (
    <Link href="/" className="brand">
      {content}
    </Link>
  ) : (
    <span className="brand">{content}</span>
  );
}
