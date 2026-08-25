import { useEffect, useState, type FormEvent } from "react";
import { Boxes, Plus, X } from "lucide-react";
import { APIError, api, type AgentApp, type Identity } from "./api";
import { AsyncState } from "./AsyncState";

export function AppsPage({ identity }: { identity: Identity }) {
  const [items, setItems] = useState<AgentApp[]>(); const [creating, setCreating] = useState(false); const [error, setError] = useState<APIError>();
  const load = () => { setError(undefined); api.apps().then(({ items }) => setItems(items)).catch(setError); };
  useEffect(load, [identity.active_tenant_id]);
  const create = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); const data = new FormData(event.currentTarget); try { const app = await api.createApp({ id: String(data.get("id")), name: String(data.get("name")) }); setItems((current) => [...(current ?? []), app]); setCreating(false); } catch (caught) { setError(caught as APIError); } };
  if (!items && !error) return <AsyncState kind="loading" />; if (error?.status === 403 && !creating) return <AsyncState kind="forbidden" />; if (error && !creating) return <AsyncState kind="error" retry={load} />;
  const mutable = identity.active_role === "platform_admin" || identity.active_role === "tenant_admin";
  return <section className="resource-list"><div className="section-toolbar"><span>{items?.length ?? 0} 个 Agent 应用</span>{mutable && <button className="primary" onClick={() => setCreating(true)}><Plus />新建应用</button>}</div>
    {!items?.length ? <AsyncState kind="empty" /> : <div className="table-wrap"><table><thead><tr><th>名称</th><th>标识</th><th>租户</th></tr></thead><tbody>{items.map((app) => <tr key={app.id}><td><Boxes />{app.name}</td><td><code>{app.id}</code></td><td><code>{app.tenant_id}</code></td></tr>)}</tbody></table></div>}
    {creating && <div className="modal-backdrop"><form className="modal" onSubmit={(event) => void create(event)}><div className="modal-title"><h3>新建 Agent 应用</h3><button type="button" className="icon-button" title="关闭" onClick={() => setCreating(false)}><X /></button></div><label>应用标识<input name="id" required pattern="[a-z][a-z0-9-]{2,62}" /></label><label>显示名称<input name="name" required minLength={2} maxLength={80} /></label>{error && <p className="form-error">{error.message}</p>}<div className="form-actions"><button type="button" onClick={() => setCreating(false)}>取消</button><button className="primary">创建应用</button></div></form></div>}
  </section>;
}
