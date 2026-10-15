import Link from "next/link";
import { adminFetch, type Me } from "@/lib/admin-api";
import { logout } from "../actions";

/**
 * Every page in this group needs a valid admin session: the layout asks the
 * admin API who is signed in (a rejected session ends at the login page).
 */
export default async function AdminLayout({ children }: LayoutProps<"/">) {
  const me = await adminFetch<Me>("/admin/v1/me");
  return (
    <>
      <a className="skip" href="#main">
        Skip to content
      </a>
      <header className="topbar">
        <nav aria-label="Main">
          <Link href="/" className="brand">
            Email Platform
          </Link>
        </nav>
        <form action={logout} className="user">
          <span>
            Signed in as <strong>{me.username}</strong>
          </span>
          <button type="submit">Sign out</button>
        </form>
      </header>
      <main id="main">{children}</main>
    </>
  );
}
