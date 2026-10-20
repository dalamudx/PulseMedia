import { useEffect, useEffectEvent, useLayoutEffect, useRef, useState } from "react";
import { Pause, Play, Trash2 } from "lucide-react";
import {
  appendRecord,
  applyMinimumLevel,
  formatLogTime,
  isLogLevel,
  levelRank,
  logLevels,
  logStreamURL,
  parseLogRecord,
  sticksToBottom,
  type LiveLogRecord,
  type LogLevel,
} from "./liveLogs";

type Connection = "connecting" | "open" | "closed";

/**
 * Live system log viewer. Logs arrive only from the authenticated SSE stream;
 * nothing is buffered in browser storage. Pausing keeps the connection open but
 * stops appending so a burst cannot scroll the view away.
 */
export function LiveLogPanel({ userID, role }: { userID: string; role: string }) {
  const [level, setLevel] = useState<LogLevel>("INFO");
  const [records, setRecords] = useState<LiveLogRecord[]>([]);
  const [connection, setConnection] = useState<Connection>("connecting");
  const [paused, setPaused] = useState(false);
  const [error, setError] = useState("");
  const box = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  const onRecord = useEffectEvent((raw: string) => {
    if (paused) return;
    const record = parseLogRecord(raw);
    if (!record) return;
    setRecords(current => appendRecord(current, record, level));
  });

  useEffect(() => {
    stick.current = true;
    setRecords([]);
    setError("");
    setConnection("connecting");
    setPaused(false);
    if (role !== "admin") {
      setConnection("closed");
      return;
    }
    const source = new EventSource(logStreamURL(level));
    source.addEventListener("connected", () => {
      setConnection("open");
      setError("");
    });
    source.addEventListener("log", event => onRecord((event as MessageEvent<string>).data));
    source.onerror = () => {
      setConnection("closed");
      setError("日志流连接已中断，正在由浏览器自动重连。");
    };
    return () => source.close();
    // The stream URL carries the level, so a change reconnects and re-reads.
  }, [userID, role, level]);

  useLayoutEffect(() => {
    const element = box.current;
    if (!element || !stick.current) return;
    element.scrollTop = element.scrollHeight;
  }, [records]);

  if (role !== "admin") {
    return (
      <div className="card logs-card">
        <h3>实时系统运行日志</h3>
        <p className="config-description" role="status">实时日志包含全进程运行信息，仅当前管理员可查看。</p>
      </div>
    );
  }

  return (
    <div className="card logs-card">
      <div className="card-header">
        <div className="logs-header-left">
          <h3>实时系统运行日志</h3>
          <span className={`badge ${connection === "open" ? "online" : "offline"}`} role="status">
            {connection === "open" ? "实时推送中" : connection === "connecting" ? "正在连接…" : "连接已中断"}
          </span>
        </div>
        <div className="logs-actions">
          <label htmlFor="live-log-level">等级过滤</label>
          <select
            id="live-log-level"
            value={level}
            onChange={event => {
              const value = event.target.value;
              if (!isLogLevel(value)) return;
              setLevel(value);
              setRecords(current => applyMinimumLevel(current, value));
            }}
          >
            {logLevels.map(entry => (
              <option key={entry} value={entry}>{entry}</option>
            ))}
          </select>
          <button type="button" className="btn-sm btn-secondary" onClick={() => setPaused(value => !value)} aria-pressed={paused}>
            {paused ? <Play size={13} /> : <Pause size={13} />}
            <span>{paused ? "继续滚动" : "暂停滚动"}</span>
          </button>
          <button type="button" className="btn-sm btn-secondary" onClick={() => setRecords([])}>
            <Trash2 size={13} />
            <span>清空屏幕</span>
          </button>
        </div>
      </div>
      {error && <div className="config-error" role="alert">{error}</div>}
      {paused && <p className="config-description" role="status">已暂停自动滚动，新日志仍会到达但暂不追加显示。</p>}
      <div
        className="terminal-box"
        ref={box}
        role="log"
        aria-live="polite"
        aria-label="实时系统日志"
        onScroll={event => {
          const element = event.currentTarget;
          stick.current = sticksToBottom(element.scrollTop, element.scrollHeight, element.clientHeight);
        }}
      >
        {records.length === 0 ? (
          <div className="log-empty">{connection === "open" ? "已连接，等待新的日志输出…" : "正在等待系统日志输出…"}</div>
        ) : (
          records.map((record, index) => (
            <div key={`${record.at}-${index}`} className="log-line" data-level={record.level}>
              <span className="log-time">{formatLogTime(record.at)}</span>
              <span className={`log-level log-level-${record.level.toLowerCase()}`}>{record.level}</span>
              <span className="log-text">{record.line}</span>
            </div>
          ))
        )}
      </div>
      <p className="config-description">
        共 {records.length} 行（最多保留 {500} 行）。日志已在服务端脱敏，凭据、下载链接与令牌不会出现。
        {connection === "open" && <span> 连接状态：{levelRank(level) >= 0 ? "正常" : "异常"}</span>}
      </p>
    </div>
  );
}
