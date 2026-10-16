import Link from "next/link";
import { adminFetch, type Me } from "@/lib/admin-api";
import { logout } from "../actions";
import { MainNav } from "./nav";

/**
 * The shell of the signed-in console. It shows who is signed in; it is not
 * the session check of the pages below (layouts do not re-run on client
 * navigation): every page loads its data through adminFetch, which is.
 */
export default async function AdminLayout({ children }: LayoutProps<"/">) {
  const me = await adminFetch<Me>("/admin/v1/me");
  return (
    <>
      <a className="skip" href="#main">
        Skip to content
      </a>
      <header className="topbar">
        <Link href="/" className="brand">
          Email Platform
        </Link>
        <MainNav />
        <form action={logout} className="user">
          <span>
            Signed in as <strong className="wrap">{me.username}</strong>
          </span>
          <button type="submit">Sign out</button>
        </form>
      </header>
      <main id="main">{children}</main>
    </>
  );
}
