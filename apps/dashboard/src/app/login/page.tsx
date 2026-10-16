import type { Metadata } from "next";
import { LoginForm } from "./login-form";

export const metadata: Metadata = { title: "Sign in" };

/**
 * The login page never redirects away because a cookie exists: a stale or
 * revoked cookie must not loop between here and the protected pages.
 */
export default async function LoginPage({ searchParams }: PageProps<"/login">) {
  const { expired } = await searchParams;
  return (
    <main className="login">
      <h1>Sign in</h1>
      <p className="muted">Email Platform operator console</p>
      {expired ? (
        <p className="notice">Your session ended. Please sign in again.</p>
      ) : null}
      <LoginForm />
    </main>
  );
}
