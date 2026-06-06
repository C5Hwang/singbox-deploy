import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// The monitor UI is served under /monitor/ by Nginx (reverse-proxied to the
// monitor service), so assets use relative paths.
export default defineConfig({
  plugins: [vue()],
  base: "./",
  build: {
    outDir: "../../assets/monitor-ui",
    emptyOutDir: true,
  },
});
