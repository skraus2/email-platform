import { test as setup } from "@playwright/test";
import { login } from "./fixtures";

// One signed-in session shared by the data specs; specs that end or revoke
// a session sign in on their own.
setup("sign in", async ({ page }) => {
  await login(page);
  await page.context().storageState({ path: "e2e/.auth/admin.json" });
});
