import { defineConfig, globalIgnores } from "eslint/config";
import tseslint from "typescript-eslint";

export default defineConfig([
  // The strictest type-aware rule sets (forgotten awaits, unsafe any,
  // conditions that are always true, ...).
  tseslint.configs.strictTypeChecked,
  tseslint.configs.stylisticTypeChecked,
  {
    languageOptions: {
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
    rules: {
      // Object shapes are declared with `type`; keep one style.
      "@typescript-eslint/consistent-type-definitions": ["error", "type"],
      // Numbers render the same everywhere; null, undefined, booleans, any
      // and objects in template literals stay errors. Every option is
      // listed: options left out fall back to the rule's lax defaults.
      "@typescript-eslint/restrict-template-expressions": [
        "error",
        {
          allowAny: false,
          allowBoolean: false,
          allowNever: false,
          allowNullish: false,
          allowNumber: true,
          allowRegExp: false,
        },
      ],
      // node:test registers tests synchronously; the returned promise
      // carries nothing the file must await.
      "@typescript-eslint/no-floating-promises": [
        "error",
        {
          allowForKnownSafeCalls: [
            {
              from: "package",
              package: "node:test",
              name: ["test", "describe", "it", "after"],
            },
          ],
        },
      ],
    },
  },
  {
    // The client never logs: apps decide what reaches their logs.
    files: ["src/**/*.ts"],
    rules: { "no-console": "error" },
  },
  {
    files: ["**/*.{js,mjs,cjs}"],
    extends: [tseslint.configs.disableTypeChecked],
  },
  globalIgnores(["dist/**", "src/generated/**"]),
]);
