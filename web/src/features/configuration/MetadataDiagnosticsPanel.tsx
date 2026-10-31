import {useEffect, useRef, useState} from "react";
import {api} from "@/api/client";
import {useAuth} from "@/features/auth/AuthContext";

type Target = "tmdb_api" | "tmdb_image" | "fanart_api" | "tvdb_api";
const targetLabels: Record<Target, string> = {tmdb_api: "TMDB API", tmdb_image: "TMDB 图片", fanart_api: "Fanart API", tvdb_api: "TVDB API"};
const messages = {
  available: "测试通过",
  host_response: "主机有响应，未验证服务可用",
  auth_required: "主机有响应，认证被拒绝",
  rate_limited: "请求受限，请稍后手动测试",
  timeout: "测试超时",
  cancelled: "测试已取消",
  unavailable: "连接不可用",
  bad_response: "响应未通过内容校验",
  not_configured: "尚未配置或启用此来源",
  configuration_changed: "配置已变化，本次结果已丢弃",
} as const;
type Result = {target: Target; category: keyof typeof messages; elapsed_ms: number; host_responded: boolean; image_retrieved: boolean};
function validResult(value: unknown, target: Target): value is Result {
  if (!value || typeof value !== "object") return false;
  const v = value as Record<string, unknown>;
  return v.target === target && typeof v.category === "string" && Object.hasOwn(messages, v.category) &&
    typeof v.elapsed_ms === "number" && Number.isSafeInteger(v.elapsed_ms) && v.elapsed_ms >= 0 &&
    typeof v.host_responded === "boolean" && typeof v.image_retrieved === "boolean" &&
    (!v.image_retrieved || (target === "tmdb_image" && v.host_responded && v.category === "available"));
}

export function MetadataDiagnosticsPanel() {
  const {user, checking} = useAuth();
  if (checking || !user || user.role !== "admin") return null;
  return <MetadataDiagnosticControls key={`${user.id}:${user.role}`} />;
}

function MetadataDiagnosticControls() {
  const [busy, setBusy] = useState<Target | null>(null);
  const [result, setResult] = useState<Result | null>(null);
  const [failure, setFailure] = useState("");
  const flight = useRef<AbortController | null>(null);
  const mounted = useRef(false);
  useEffect(() => {
    mounted.current = true;
    return () => {mounted.current = false; flight.current?.abort(); flight.current = null;};
  }, []);
  const run = async (target: Target) => {
    if (!mounted.current || flight.current) return;
    const controller = new AbortController();
    flight.current = controller;
    setBusy(target); setResult(null); setFailure("");
    try {
      const {data, error, response} = await api.POST("/api/v1/settings/metadata-diagnostics", {
        body: {target}, signal: controller.signal, cache: "no-store",
      });
      if (!mounted.current || flight.current !== controller || controller.signal.aborted) return;
      if (error || !validResult(data, target)) {
        setFailure(response.status === 401 || response.status === 403 ? "当前会话无权测试，请重新核对账户。" : "测试结果无法确认，请手动重试。");
      } else setResult(data);
    } catch {
      if (mounted.current && flight.current === controller && !controller.signal.aborted) setFailure("测试结果无法确认，请手动重试。");
    } finally {
      if (flight.current === controller) {
        flight.current = null;
        if (mounted.current) setBusy(null);
      }
    }
  };
  return <section className="card" aria-labelledby="metadata-diagnostics-title">
    <div className="card-header"><h3 id="metadata-diagnostics-title">元数据连接测试</h3></div>
    <p className="card-desc">按当前配置测试元数据与图片来源。仅在点击后发送请求。</p>
    <div style={{display: "flex", flexWrap: "wrap", gap: "12px"}}>
      {(Object.keys(targetLabels) as Target[]).map(target => <button key={target} type="button" className="btn-secondary" style={{minWidth:44,minHeight:44}} disabled={busy !== null} onClick={() => void run(target)}>{busy === target ? "正在测试…" : `测试 ${targetLabels[target]}`}</button>)}
    </div>
    <div role="status" aria-live="polite">
      {result && <p>{targetLabels[result.target]}：{messages[result.category]} · {result.elapsed_ms} 毫秒。{result.host_responded ? "主机已响应。" : "未确认主机响应。"}{result.target === "tmdb_image" && (result.image_retrieved ? "图片已成功取回并校验。" : "未成功取回图片。")}</p>}
      {failure && <p>{failure}</p>}
    </div>
  </section>;
}
