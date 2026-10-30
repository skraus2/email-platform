"use client";

import { useActionState, useEffect, useRef } from "react";
import { login, type LoginState } from "../actions";

export function LoginForm() {
  const [state, action, pending] = useActionState<LoginState, FormData>(
    login,
    undefined,
  );
  const password = useRef<HTMLInputElement>(null);
  const invalid = state?.kind === "credentials";

  // After a failed attempt focus returns to the (cleared) password field,
  // which reads the error through aria-describedby.
  useEffect(() => {
    if (state?.attempt) {
      password.current?.focus();
    }
  }, [state?.attempt]);

  return (
    <form
      action={action}
      className="stack"
      onSubmit={(e) => {
        if (pending) {
          e.preventDefault();
        }
      }}
    >
      <label htmlFor="username">Username</label>
      <input
        id="username"
        name="username"
        autoComplete="username"
        autoCapitalize="none"
        autoCorrect="off"
        spellCheck={false}
        required
        maxLength={64}
        defaultValue={state?.username}
        key={state?.username ?? ""}
        aria-invalid={invalid || undefined}
        aria-describedby={state ? "login-error" : undefined}
      />
      <label htmlFor="password">Password</label>
      <input
        ref={password}
        id="password"
        name="password"
        type="password"
        autoComplete="current-password"
        required
        aria-invalid={invalid || undefined}
        aria-describedby={state ? "login-error" : undefined}
      />
      {state ? (
        <p key={state.attempt} id="login-error" className="error" role="alert">
          {state.error}
        </p>
      ) : null}
      {/* Kept enabled while pending so focus stays on it; a second submit is ignored. */}
      <button
        type="submit"
        className="primary"
        aria-disabled={pending || undefined}
      >
        {pending ? "Signing in…" : "Sign in"}
      </button>
      <p className="visually-hidden" role="status">
        {pending ? "Signing in…" : ""}
      </p>
    </form>
  );
}
