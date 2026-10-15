import type { Metadata } from "next";
import { LoginForm } from "./login-form";

export const metadata: Metadata = { title: "Sign in · Email Platform" };

/**
 * The login page never redirects away because a cookie exists: a stale or
 * revoked cookie must not loop between here and the protected pages.
 */
export default async function LoginPage({ searchParams }: PageProps<"/login">) {
  const { expired } = await searchParams;
  return (
    <main className="login">
      <h1>Email Platform</h1>
      <p className="muted">Operator sign-in</p>
      {expired ? (
        <p className="notice" role="status">
          Your session ended. Please sign in again.
        </p>
      ) : null}
      <LoginForm />
    </main>
  );
}
