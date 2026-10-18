import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath, URL } from "node:url";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const backendTarget = env.PULSE_BACKEND_URL || env.VITE_API_URL || "http://127.0.0.1:8088";
  const devHost = env.VITE_HOST || env.HOST || "0.0.0.0";
  const devPort = parseInt(env.VITE_PORT || "3000", 10);

  return {
    plugins: [react()],
    resolve: {
      alias: {
        "@": fileURLToPath(new URL("./src", import.meta.url)),
      },
    },
    server: {
      host: devHost,
      port: devPort,
      proxy: {
        "/api": {
          target: backendTarget,
          changeOrigin: true,
        },
        "/healthz": {
          target: backendTarget,
        },
        "/readyz": {
          target: backendTarget,
        },
      },
    },
    build: {
      outDir: "dist",
      emptyOutDir: true,
    },
  };
});
