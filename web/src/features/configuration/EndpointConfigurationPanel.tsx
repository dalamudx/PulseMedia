import { useEndpointConfiguration, type EndpointFamily } from "./useEndpointConfiguration";
import "./configuration.css";

export function EndpointConfigurationPanel({ family }: { family: EndpointFamily }) {
  const model = useEndpointConfiguration(family);
  const download = family === "download_client", label = download ? "下载器" : "媒体服务器";
  const { draft, editing } = model;
  const prefix = `endpoint-${family}`;
  const options = download ? [["qbittorrent", "qBittorrent"], ["transmission", "Transmission"]] : [["jellyfin", "Jellyfin"], ["emby", "Emby"]];
  const legacy = !options.some(([value]) => value === draft.type);
  const reread = model.gate === "conflict";
  if (!model.isAdmin) return <div className="card"><p className="card-desc">仅管理员可管理{label}配置。</p></div>;

  return <section className="card" aria-labelledby={`${prefix}-title`} aria-busy={model.busy !== null}>
    <div className="card-header config-heading">
      <h3 id={`${prefix}-title`}>{label}配置{download ? " (qBittorrent / Transmission)" : " (Jellyfin / Emby)"}</h3>
      <button type="button" className="btn-sm btn-secondary" disabled={model.busy !== null} onClick={() => { void (reread ? model.reread() : model.refresh()); }}>
        {model.busy === "read" ? "读取中..." : reread ? "重新读取（放弃草稿）" : model.gate === "unknown" ? "重新读取并核对" : model.listStatus === "error" || model.gate === "saved" ? "重新读取" : "刷新列表"}
      </button>
    </div>
    <p className="card-desc">{download ? "配置下载任务使用的下载器，并测试服务连接。" : "配置整理入库后使用的媒体服务器，并测试服务连接。"}</p>
    {model.message ? <p className="config-feedback" role={model.feedbackKind === "error" ? "alert" : "status"}>{model.message}</p> : null}
    {model.listStatus === "loading" ? <p role="status">正在读取配置列表...</p> : model.listStatus === "error" ? null : model.items.length === 0 ? <div className="empty-hint">暂无配置{label}，请在下方添加。</div> : <div className="integration-list">
      {model.items.map(item => <div className="integration-row" key={item.id}>
        <div>
          <strong>{item.name}</strong> <span className="badge server-badge">{options.find(([type]) => type === item.type)?.[1] ?? `${item.type}（已有配置）`}</span>
          <div className="item-meta">{item.url}</div>
          {model.probes[item.id] ? <div className="test-tip" role={model.probes[item.id].kind === "error" ? "alert" : "status"}>{model.probes[item.id].message}</div> : null}
        </div>
        <div className="item-actions">
          <button type="button" className="btn-sm btn-primary" disabled={model.blocked} onClick={() => { void model.probe(item.id); }} aria-label={`测试连接：${item.name}`}>{model.busy === "probe" ? "测试中..." : "测试连接"}</button>
          <button type="button" className="btn-sm btn-secondary" disabled={model.blocked} onClick={() => model.startEdit(item.id)}>编辑</button>
          <button type="button" className="btn-sm btn-danger" disabled={model.blocked} onClick={() => { if (window.confirm(`确定要删除该${label}吗？`)) void model.remove(item.id); }}>删除</button>
        </div>
      </div>)}
    </div>}
    <div className="config-editor">
      <h4 id={`${prefix}-edit-title`} className="sub-heading">{editing ? `编辑${label}：${draft.name}` : `添加${label}`}</h4>
      <form onSubmit={event => { event.preventDefault(); void model.save(); }}>
        <fieldset className="config-fieldset" disabled={model.blocked} aria-labelledby={`${prefix}-edit-title`}>
          <div className="form-grid">
            <div className="form-group">
              <label htmlFor={`${prefix}-type`}>{download ? "下载器类型" : "服务器类型"}</label>
              <select id={`${prefix}-type`} value={draft.type} onChange={event => model.change("type", event.target.value)}>
                {legacy ? <option value={draft.type} disabled>{draft.type}（已有配置）</option> : null}
                {options.map(([value, name]) => <option key={value} value={value}>{name}</option>)}
              </select>
            </div>
            <div className="form-group">
              <label htmlFor={`${prefix}-name`}>{label}名称</label>
              <input id={`${prefix}-name`} type="text" required value={draft.name} onChange={event => model.change("name", event.target.value)} />
            </div>
            <div className="form-group">
              <label htmlFor={`${prefix}-url`}>{download ? "Web UI 地址" : "服务地址 URL"}</label>
              <input id={`${prefix}-url`} type="url" required autoComplete="off" spellCheck={false} value={draft.url} onChange={event => model.change("url", event.target.value)} aria-describedby={`${prefix}-url-help`} />
              <p id={`${prefix}-url-help`} className="config-description">支持明确配置的本地 HTTP(S) 地址。含 *** 的地址可原样保留；修改时请填写完整地址。</p>
            </div>
            {download ? <div className="form-group">
              <label htmlFor={`${prefix}-username`}>Web 账号</label>
              <input id={`${prefix}-username`} type="text" autoComplete="off" value={draft.username} onChange={event => model.change("username", event.target.value)} />
            </div> : null}
            <div className="form-group">
              <label htmlFor={`${prefix}-secret`}>{download ? "Web 密码" : "API Key / Token"}{editing ? "（留空保留已有值）" : "（可留空）"}</label>
              <input id={`${prefix}-secret`} type="password" autoComplete="new-password" value={download ? draft.password : draft.api_key} onChange={event => model.change(download ? "password" : "api_key", event.target.value)} />
            </div>
            <div className="form-actions-full config-actions">
              <button type="submit" className="btn-primary" disabled={model.blocked}>{model.busy === "save" ? "保存中..." : editing ? `保存${label}修改` : `保存并启用${label}`}</button>
              {editing ? <button type="button" className="btn-secondary" disabled={model.blocked} onClick={model.cancelEdit}>取消编辑</button> : null}
            </div>
          </div>
        </fieldset>
      </form>
    </div>
  </section>;
}
