import { useEffect, useRef, useState, type FormEvent } from "react";
import { Plus, Rocket, X } from "lucide-react";
import { APIError, api, type AgentApp, type Deployment, type DeploymentVersion, type Identity } from "./api";
import { AsyncState } from "./AsyncState";

export function DeploymentsPage({ identity }: { identity: Identity }) {
  const [items, setItems] = useState<Deployment[]>(); const [apps, setApps] = useState<AgentApp[]>([]); const [selected, setSelected] = useState<Deployment>(); const [versions, setVersions] = useState<DeploymentVersion[]>([]); const [mode, setMode] = useState<"deployment" | "version">(); const [error, setError] = useState<APIError>(); const requestGeneration = useRef(0); const versionAttempt = useRef<{ deploymentID: string; configText: string; key: string } | undefined>(undefined);
  const load = () => { const generation = ++requestGeneration.current; setError(undefined); Promise.all([api.deployments(), api.apps()]).then(([d, a]) => { if (requestGeneration.current === generation) { setItems(d.items); setApps(a.items); } }).catch((caught) => { if (requestGeneration.current === generation) setError(caught); }); };
  useEffect(() => { setSelected(undefined); setVersions([]); versionAttempt.current = undefined; load(); return () => { requestGeneration.current++; }; }, [identity.active_tenant_id]);
  const open = async (deployment: Deployment) => { setSelected(deployment); try { setVersions((await api.versions(deployment.id)).items); } catch (caught) { setError(caught as APIError); } };
  const createDeployment = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); const data = new FormData(event.currentTarget); try { const item = await api.createDeployment({ id: String(data.get("id")), agent_app_id: String(data.get("agent_app_id")) }); setItems((current) => [...(current ?? []), item]); setMode(undefined); await open(item); } catch (caught) { setError(caught as APIError); } };
  const createVersion = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!selected) return;
    const configText = String(new FormData(event.currentTarget).get("config"));
    let config: Record<string, unknown>;
    try { config = JSON.parse(configText) as Record<string, unknown>; } catch { setError(new APIError(400, "invalid_json", "配置必须是有效 JSON")); return; }
    let attempt = versionAttempt.current;
    if (!attempt || attempt.deploymentID !== selected.id || attempt.configText !== configText) {
      attempt = { deploymentID: selected.id, configText, key: crypto.randomUUID() };
      versionAttempt.current = attempt;
    }
    try {
      const item = await api.createVersion(selected.id, config, attempt.key);
      versionAttempt.current = undefined;
      setVersions((current) => [...current, item]);
      setError(undefined);
      setMode(undefined);
    } catch (caught) {
      setError(caught instanceof APIError ? caught : new APIError(0, "network_error", "无法连接服务，请重试"));
    }
  };
  const transition = async (status: "published" | "active" | "paused") => {
    if (!selected) return;
    try {
      const version_id = status === "published" ? versions.at(-1)?.id : undefined;
      const updated = await api.transition(selected.id, status, version_id);
      setError(undefined);
      setSelected(updated);
      setItems((current) => current?.map((item) => item.id === updated.id ? updated : item));
    } catch (caught) {
      const apiError = caught as APIError;
      setError(apiError);
      if (apiError.code === "agent_app_already_has_active_deployment") {
        try {
          const refreshed = (await api.deployments()).items;
          setItems(refreshed);
          setSelected(refreshed.find((item) => item.id === selected.id));
        } catch {
          // Keep the original conflict visible; a later list refresh can retry state synchronization.
        }
      }
    }
  };
  if (!items && !error) return <AsyncState kind="loading" />; if (error?.status === 403 && !mode) return <AsyncState kind="forbidden" />; if (error && !mode && !items) return <AsyncState kind="error" retry={load} />;
  const canConfigure = identity.active_role === "platform_admin" || identity.active_role === "tenant_admin";
  const canOperate = canConfigure || identity.active_role === "operator";
  return <div className="resource-layout"><section className="resource-list"><div className="section-toolbar"><span>{items?.length ?? 0} 个部署</span>{canConfigure && <button className="primary" disabled={!apps.length} onClick={() => setMode("deployment")}><Plus />新建部署</button>}</div>{error && items && <p className="inline-error" role="alert">{error.message}</p>}{!items?.length ? <AsyncState kind="empty" /> : <div className="table-wrap"><table><thead><tr><th>部署</th><th>应用</th><th>状态</th></tr></thead><tbody>{items.map((item) => <tr key={item.id} onClick={() => void open(item)}><td><Rocket />{item.id}</td><td><code>{item.agent_app_id}</code></td><td><span className={`status ${item.status}`}>{item.status}</span></td></tr>)}</tbody></table></div>}</section>
    {selected && <aside className="detail-panel"><button className="icon-button" title="关闭详情" onClick={() => { versionAttempt.current = undefined; setSelected(undefined); }}><X /></button><span className="eyebrow">Deployment</span><h3>{selected.id}</h3><dl><dt>状态</dt><dd><span className={`status ${selected.status}`}>{selected.status}</span></dd><dt>当前版本</dt><dd>{selected.version_id ?? "尚未发布"}</dd><dt>版本历史</dt><dd>{versions.length ? versions.map((v) => <div className="version-record" key={v.id}><code>v{v.number}</code><pre>{JSON.stringify(v.config, null, 2)}</pre></div>) : "暂无版本"}</dd></dl>{canOperate && <div className="stack-actions">{canConfigure && <button onClick={() => { versionAttempt.current = undefined; setError(undefined); setMode("version"); }}>创建版本</button>}{selected.status === "draft" && <button disabled={!versions.length} onClick={() => void transition("published")}>发布</button>}{selected.status === "published" && <button onClick={() => void transition("active")}>激活</button>}{selected.status === "active" && <button onClick={() => void transition("paused")}>暂停</button>}</div>}</aside>}
    {mode === "deployment" && <div className="modal-backdrop"><form className="modal" onSubmit={(event) => void createDeployment(event)}><div className="modal-title"><h3>新建部署</h3><button type="button" className="icon-button" title="关闭" onClick={() => setMode(undefined)}><X /></button></div><label>部署标识<input name="id" required pattern="[a-z][a-z0-9-]{2,62}" /></label><label>Agent 应用<select name="agent_app_id">{apps.map((app) => <option key={app.id} value={app.id}>{app.name}</option>)}</select></label><div className="form-actions"><button type="button" onClick={() => setMode(undefined)}>取消</button><button className="primary">创建部署</button></div></form></div>}
    {mode === "version" && <div className="modal-backdrop"><form className="modal" onSubmit={(event) => void createVersion(event)}><div className="modal-title"><h3>创建不可变版本</h3><button type="button" className="icon-button" title="关闭" onClick={() => { versionAttempt.current = undefined; setMode(undefined); }}><X /></button></div><label>JSON 配置<textarea name="config" rows={7} defaultValue={'{"runner":"fake"}'} required /></label>{error && <p className="form-error">{error.message}</p>}<div className="form-actions"><button type="button" onClick={() => { versionAttempt.current = undefined; setMode(undefined); }}>取消</button><button className="primary">创建版本</button></div></form></div>}
  </div>;
}
