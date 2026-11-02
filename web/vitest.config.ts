import { defineConfig, mergeConfig } from "vitest/config";
import viteConfig from "./vite.config.ts";

// Reuses the app's plugins and the `@` alias, so tests resolve imports exactly as the build does.
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: "jsdom",
      setupFiles: ["src/test/setup.ts"],
      css: false,
      globals: false,
      include: ["src/**/*.test.{ts,tsx}", "vite-plugins/**/*.test.ts"],
    },
  }),
);
