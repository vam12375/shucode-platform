import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { consoleAssetAccess, consoleObfuscation } from "./build-access.mjs";

export default defineConfig({
  plugins: [react(), consoleObfuscation(), consoleAssetAccess()],
  base: "/console/",
  server: {
    port: 5177,
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
    manifest: true,
    sourcemap: false,
    // 跨权限模块不预加载，避免匿名访问时提前请求工作台或管理资源。
    modulePreload: { resolveDependencies: () => [] },
    rolldownOptions: {
      output: {
        entryFileNames: "assets/[hash].js",
        chunkFileNames: "assets/[hash].js",
        assetFileNames: "assets/[hash][extname]",
      },
    },
  },
});
