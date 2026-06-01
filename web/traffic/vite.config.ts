import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// The traffic UI is served under /traffic/ by Nginx (reverse-proxied to the
// monitor service), so assets use relative paths.
export default defineConfig({
  plugins: [vue()],
  base: "./",
  build: {
    outDir: "../../template/traffic-ui",
    emptyOutDir: true,
  },
});
