import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { AuthProvider } from "@/features/auth/AuthContext";
import { ToastProvider } from "@/features/toast/ToastContext";
import { App } from "@/App";
import { ensureRandomUUID } from "@/platform/randomUUID";
import "@/assets/main.css";
import "@/assets/workspace.css";
import "@/assets/redesign.css";

ensureRandomUUID();

const rootElement = document.getElementById("root");
if (rootElement) {
  ReactDOM.createRoot(rootElement).render(
    <React.StrictMode>
      <AuthProvider>
        <ToastProvider>
          <BrowserRouter>
            <App />
          </BrowserRouter>
        </ToastProvider>
      </AuthProvider>
    </React.StrictMode>
  );
}
