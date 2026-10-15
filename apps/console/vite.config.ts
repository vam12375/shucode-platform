import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react()],
  base: "/console/",
  server: {
    port: 5174,
    proxy: {
      // 开发期直连本地 API（pnpm dev:api）；生产由 API 以 /console 路径托管构建产物。
      "/console/api": {
        target: process.env.VITE_API_PROXY?.trim() || "http://localhost:8787",
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
  },
});
