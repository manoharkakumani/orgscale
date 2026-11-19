import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The console is served by orgscale under /admin/ and embedded in the binary
// from hscontrol/adminui/dist.
export default defineConfig({
  base: "/admin/",
  plugins: [react()],
  build: {
    outDir: "../../hscontrol/adminui/dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    // `npm run dev` proxies the API to a local orgscale.
    proxy: {
      "/admin/api": "http://127.0.0.1:8080",
      "/admin/auth": "http://127.0.0.1:8080",
      "/api": "http://127.0.0.1:8080",
    },
  },
});
