// Entry point for browser bundles: the client holds a project API key, which
// must never reach a browser, so loading it there fails.
throw new Error(
  "@email-platform/sdk is server-side only: it needs a project API key, which must not be shipped to a browser",
);
