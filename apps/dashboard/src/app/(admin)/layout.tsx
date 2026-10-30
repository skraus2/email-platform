import { Brand } from "@/components/brand";
import { Icon } from "@/components/icons";
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
    <div className="shell">
      <a className="skip" href="#main">
        Skip to content
      </a>
      <header className="sidebar">
        <Brand />
        <MainNav />
        <form action={logout} className="user">
          <span className="avatar" aria-hidden="true">
            {me.username.slice(0, 1)}
          </span>
          <span className="user-name">
            <span className="muted">Signed in as </span>
            <strong className="wrap">{me.username}</strong>
          </span>
          <button type="submit" className="ghost" title="Sign out">
            <Icon name="signOut" />
            <span className="visually-hidden">Sign out</span>
          </button>
        </form>
      </header>
      <main id="main">{children}</main>
    </div>
  );
}
