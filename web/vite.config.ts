import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  server: {
    // DASH_API overrides the backend address when 8080 is taken (server.listen / DASH_SERVER_LISTEN on the Go side).
    proxy: { "/api": process.env.DASH_API ?? "http://127.0.0.1:8080" },
  },
  // dist/.gitkeep (committed, so go:embed works before a build) comes back from public/.gitkeep.
  build: { outDir: "dist" },
});
