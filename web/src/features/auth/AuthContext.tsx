import React, { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import type { paths } from "@/api/client";
import type { Middleware } from "openapi-fetch";

type User = paths["/api/v1/auth/status"]["get"]["responses"]["200"]["content"]["application/json"]["current_user"];
interface AuthContextType {
  user: User | null;
  initialized: boolean | null;
  loading: boolean;
  checking: boolean;
  statusError: string | null;
  error: string | null;
  login: (username: string, password: string) => Promise<boolean>;
  setup: (username: string, password: string) => Promise<boolean>;
  logout: () => Promise<boolean>;
  checkStatus: () => Promise<void>;
}
const AuthContext = createContext<AuthContextType | null>(null);
const anonymousEndpoints = new Set(["/api/v1/auth/status", "/api/v1/auth/login", "/api/v1/auth/setup", "/api/v1/auth/logout"]);

export const AuthProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [user, setUser] = useState<User | null>(null);
  const [initialized, setInitialized] = useState<boolean | null>(null);
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(true);
  const [statusError, setStatusError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const mounted = useRef(false);
  const epoch = useRef(0);
  const statusReader = useRef<AbortController | null>(null);
  const writing = useRef(false);

  const checkStatus = useCallback(async () => {
    const version = ++epoch.current;
    statusReader.current?.abort();
    const controller = new AbortController();
    statusReader.current = controller;
    setChecking(true);
    setStatusError(null);
    setUser(null);
    try {
      const { data, error: failure } = await api.GET("/api/v1/auth/status", { signal: controller.signal, cache: "no-store" });
      if (!mounted.current || version !== epoch.current || controller.signal.aborted) return;
      if (failure || !data || typeof data.initialized !== "boolean") throw new Error("status unavailable");
      setInitialized(data.initialized);
      setUser(data.current_user ?? null);
    } catch {
      if (!mounted.current || version !== epoch.current || controller.signal.aborted) return;
      setInitialized(null);
      setUser(null);
      setStatusError("认证状态读取失败。请重试后继续，暂不能初始化或进入账户。");
    } finally {
      if (statusReader.current === controller) statusReader.current = null;
      if (mounted.current && version === epoch.current) setChecking(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    const requests = new Map<string, number>();
    const middleware: Middleware = {
      onRequest({ request, id }) {
        requests.set(id, epoch.current);
        if (new URL(request.url).origin === window.location.origin && !["GET", "HEAD", "OPTIONS"].includes(request.method)) {
          try {
            const value = document.cookie.split("; ").find(cookie => cookie.startsWith("csrf_token="))?.slice("csrf_token=".length);
            if (value) request.headers.set("X-CSRF-Token", decodeURIComponent(value));
          } catch { /* An invalid cookie cannot authorize a write. */ }
        }
        return request;
      },
      onResponse({ request, response, id }) {
        const version = requests.get(id);
        requests.delete(id);
        if (mounted.current && version === epoch.current && response.status === 401 && !anonymousEndpoints.has(new URL(request.url).pathname)) {
          ++epoch.current;
          statusReader.current?.abort();
          setUser(null);
          setInitialized(true);
          setChecking(false);
          setLoading(false);
          setStatusError(null);
          setError("会话已失效，请重新登录。");
        }
        return response;
      },
      onError({ id }) { requests.delete(id); },
    };
    api.use(middleware);
    void checkStatus();
    return () => {
      mounted.current = false;
      ++epoch.current;
      statusReader.current?.abort();
      api.eject(middleware);
      requests.clear();
    };
  }, [checkStatus]);

  async function authenticate(kind: "login" | "setup", username: string, password: string): Promise<boolean> {
    if (writing.current) return false;
    writing.current = true;
    const version = ++epoch.current;
    statusReader.current?.abort();
    setLoading(true);
    setError(null);
    try {
      const result = kind === "login"
        ? await api.POST("/api/v1/auth/login", { body: { username, password } })
        : await api.POST("/api/v1/auth/setup", { body: { username, password } });
      if (!mounted.current || version !== epoch.current) return false;
      if (result.response.status >= 500) throw new Error("authentication unconfirmed");
      if (result.error || !result.data?.user) {
        setError(result.error?.message || (kind === "login" ? "登录失败" : "初始化失败"));
        return false;
      }
      setInitialized(true);
      setUser(result.data.user);
      setStatusError(null);
      setChecking(false);
      return true;
    } catch {
      if (mounted.current && version === epoch.current) {
        setUser(null);
        setInitialized(null);
        setChecking(false);
        setStatusError("认证操作结果未确认。请重新读取认证状态后继续。");
      }
      return false;
    } finally {
      writing.current = false;
      if (mounted.current && version === epoch.current) setLoading(false);
    }
  }
  const login = (username: string, password: string) => authenticate("login", username, password);
  const setup = (username: string, password: string) => authenticate("setup", username, password);

  async function logout(): Promise<boolean> {
    if (writing.current) return false;
    writing.current = true;
    const version = ++epoch.current;
    statusReader.current?.abort();
    setLoading(true);
    setError(null);
    try {
      const { error: failure, response } = await api.POST("/api/v1/auth/logout");
      if (!mounted.current || version !== epoch.current) return false;
      if (failure && response.status !== 401) throw new Error("logout unconfirmed");
      setUser(null);
      setInitialized(true);
      return true;
    } catch {
      if (mounted.current && version === epoch.current) {
        setUser(null);
        setInitialized(null);
        setChecking(false);
        setStatusError("退出结果尚未确认。请重新读取认证状态；当前账户内容已清除。");
      }
      return false;
    } finally {
      writing.current = false;
      if (mounted.current && version === epoch.current) setLoading(false);
    }
  }

  return <AuthContext.Provider value={{ user, initialized, loading, checking, statusError, error, login, setup, logout, checkStatus }}>{children}</AuthContext.Provider>;
};
export const useAuth = () => {
  const context = useContext(AuthContext);
  if (!context) throw new Error("useAuth must be used within an AuthProvider");
  return context;
};
