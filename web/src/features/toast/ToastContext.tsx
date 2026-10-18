import React, { createContext, useContext, useState, useCallback } from "react";
import { CheckCircle2, AlertCircle, AlertTriangle, Info, X } from "lucide-react";

export interface ToastItem {
  id: string;
  type: "success" | "error" | "warning" | "info";
  title: string;
  message?: string;
}

interface ToastContextType {
  success: (title: string, message?: string) => void;
  error: (title: string, message?: string) => void;
  warning: (title: string, message?: string) => void;
  info: (title: string, message?: string) => void;
  remove: (id: string) => void;
}

const ToastContext = createContext<ToastContextType | null>(null);

export const ToastProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [toasts, setToasts] = useState<ToastItem[]>([]);

  const remove = useCallback((id: string) => {
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  const add = useCallback((type: ToastItem["type"], title: string, message?: string, duration = 4000) => {
    const id = Math.random().toString(36).substring(2, 9);
    setToasts((prev) => [...prev, { id, type, title, message }]);
    if (duration > 0) {
      setTimeout(() => {
        remove(id);
      }, duration);
    }
  }, [remove]);

  const success = useCallback((t: string, m?: string) => add("success", t, m), [add]);
  const error = useCallback((t: string, m?: string) => add("error", t, m, 5000), [add]);
  const warning = useCallback((t: string, m?: string) => add("warning", t, m), [add]);
  const info = useCallback((t: string, m?: string) => add("info", t, m), [add]);

  return (
    <ToastContext.Provider value={{ success, error, warning, info, remove }}>
      {children}
      {/* Toast Notification Container */}
      <div className="toast-container">
        {toasts.map((t) => (
          <div key={t.id} className={`toast-card ${t.type}`} onClick={() => remove(t.id)}>
            <div className="toast-icon">
              {t.type === "success" && <CheckCircle2 size={18} color="#10b981" />}
              {t.type === "error" && <AlertCircle size={18} color="#ef4444" />}
              {t.type === "warning" && <AlertTriangle size={18} color="#f59e0b" />}
              {t.type === "info" && <Info size={18} color="#6366f1" />}
            </div>
            <div className="toast-text">
              <strong>{t.title}</strong>
              {t.message && <p>{t.message}</p>}
            </div>
            <button className="toast-close-btn" onClick={(e) => { e.stopPropagation(); remove(t.id); }}>
              <X size={14} />
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
};

export const useToast = () => {
  const context = useContext(ToastContext);
  if (!context) {
    throw new Error("useToast must be used within a ToastProvider");
  }
  return context;
};
