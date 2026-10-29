import path from "node:path";

/**
 * The signed-in browser state shared by the data specs. The run script keeps
 * it in its temporary run directory, which it removes afterwards.
 */
export const AUTH_STATE =
  process.env["E2E_AUTH_STATE"] ?? path.join(__dirname, ".auth", "admin.json");
