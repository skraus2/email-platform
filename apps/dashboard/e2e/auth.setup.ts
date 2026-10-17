import { test as setup } from "@playwright/test";
import { login } from "./fixtures";
import { AUTH_STATE } from "./paths";

// One signed-in session shared by the data specs; specs that end or revoke
// a session sign in on their own.
setup("sign in", async ({ page }) => {
  await login(page);
  await page.context().storageState({ path: AUTH_STATE });
});
