import { useEffect, useState, type FormEvent } from "react";
import { Plug, Plus, Trash2 } from "lucide-react";
import { api, type APIError, type AgentApp, type BotRoute, type ChannelBinding, type Identity, type ProviderStatus } from "./api";
import { AsyncState } from "./AsyncState";

export function ChannelsPage({ identity }: { identity: Identity }) {
  const [items, setItems] = useState<ChannelBinding[]>();
  const [apps, setApps] = useState<AgentApp[]>([]);
  const [providers, setProviders] = useState<ProviderStatus[]>([]);
  const [routes, setRoutes] = useState<BotRoute[]>([]);
  const [error, setError] = useState<APIError>();
  const [channel, setChannel] = useState<BotRoute["provider"]>("enterprise_wechat");
  const [appID, setAppID] = useState("");
  const [conversation, setConversation] = useState("");
  const mutable = ["platform_admin", "tenant_admin", "operator"].includes(identity.active_role);
  const load = () => { setError(undefined); void Promise.all([api.bindings(), api.apps(), api.providerStatuses(), api.providerRoutes()]).then(([bindings, appResult, providerResult, routeResult]) => { setItems(bindings.items); setApps(appResult.items); setProviders(providerResult.items); setRoutes(routeResult.items); setAppID((current) => current || appResult.items[0]?.id || ""); }).catch((caught) => setError(caught as APIError)); };
  useEffect(load, [identity.active_tenant_id]);
  const create = async (event: FormEvent) => { event.preventDefault(); if (identity.active_role !== "platform_admin" || !appID || !conversation) return; try { await api.createProviderRoute({ provider: channel, external_subject: conversation, tenant_id: identity.active_tenant_id, app_id: appID, conversation_type: "single" }); setConversation(""); load(); } catch (caught) { setError(caught as APIError); } };
  if (!items) return <AsyncState kind="loading" />;
  const toggle = async (binding: ChannelBinding) => { try { await api.setBindingEnabled(binding.id, !binding.enabled); load(); } catch (caught) { setError(caught as APIError); } };
  const remove = async (id: string) => { try { await api.deleteBinding(id); load(); } catch (caught) { setError(caught as APIError); } };
  return <div className="channels-page">
    {error && <div className="inline-error">{error.message}</div>}
    <div className="runtime-grid">{providers.map((provider) => <div className="runtime-item" key={provider.provider}><div className="runtime-title"><Plug aria-hidden="true" /><div><h3>{provider.provider}</h3><span>Provider Account</span></div><span className={`status ${provider.status === "connected" ? "healthy" : "paused"}`}>{provider.status}</span></div></div>)}</div>
    <form className="channel-form" onSubmit={(event) => void create(event)}>
      <label>Provider<select value={channel} onChange={(event) => setChannel(event.target.value as BotRoute["provider"])} disabled={!mutable}><option value="enterprise_wechat">企业微信</option><option value="telegram">Telegram</option></select></label>
      <label>Agent 应用<select value={appID} onChange={(event) => setAppID(event.target.value)} disabled={!mutable}>{apps.map((app) => <option value={app.id} key={app.id}>{app.name}</option>)}</select></label>
      <label>会话 ID<input value={conversation} onChange={(event) => setConversation(event.target.value)} disabled={!mutable} /></label>
      <button className="primary" type="submit" disabled={identity.active_role !== "platform_admin" || !appID || !conversation}><Plus aria-hidden="true" />创建路由</button>
    </form>
    {routes.length > 0 && <div className="table-wrap"><table><thead><tr><th>Bot Provider</th><th>外部会话</th><th>Tenant</th><th>Agent App</th></tr></thead><tbody>{routes.map((route) => <tr key={`${route.provider}:${route.external_subject}`}><td><Plug aria-hidden="true" />{route.provider}</td><td>{route.external_subject}</td><td>{route.tenant_id}</td><td>{route.app_id}</td></tr>)}</tbody></table></div>}
    <div className="table-wrap"><table><thead><tr><th>Provider</th><th>会话</th><th>用户</th><th>状态</th><th>操作</th></tr></thead><tbody>{items.map((binding) => <tr key={binding.id}><td><Plug aria-hidden="true" />{binding.channel}</td><td>{binding.external_conversation_id}</td><td>{binding.external_user_id}</td><td><span className={`status ${binding.enabled ? "active" : "paused"}`}>{binding.enabled ? "启用" : "停用"}</span></td><td><button className="icon-button" title={binding.enabled ? "停用" : "启用"} onClick={() => void toggle(binding)} disabled={!mutable}>{binding.enabled ? "停用" : "启用"}</button><button className="icon-button" title="删除" onClick={() => void remove(binding.id)} disabled={!mutable}><Trash2 aria-hidden="true" /></button></td></tr>)}</tbody></table></div>
  </div>;
}
