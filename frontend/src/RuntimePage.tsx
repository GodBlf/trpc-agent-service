import { useEffect, useState } from "react";
import { Activity, Server } from "lucide-react";
import { APIError, api, type RuntimeStatus } from "./api";
import { AsyncState } from "./AsyncState";

export function RuntimePage() {
  const [items, setItems] = useState<RuntimeStatus[]>(); const [error, setError] = useState<APIError>();
  const load = () => { setError(undefined); api.runtimeStatus().then(({ items }) => setItems(items)).catch(setError); };
  useEffect(load, []);
  if (!items && !error) return <AsyncState kind="loading" />; if (error?.status === 403) return <AsyncState kind="forbidden" />; if (error) return <AsyncState kind="error" retry={load} />; if (!items?.length) return <AsyncState kind="empty" />;
  return <div className="runtime-grid">{items.map((item) => <article className="runtime-item" key={item.id}><div className="runtime-title">{item.role === "gateway" ? <Activity /> : <Server />}<div><h3>{item.id}</h3><span>{item.role}</span></div><span className={`status ${item.lifecycle}`}>{item.lifecycle}</span></div><dl><dt>可用性</dt><dd>{item.available ? "可用" : "不可用"}</dd><dt>正在执行</dt><dd>{item.active_executions}</dd><dt>已完成</dt><dd>{item.completed_executions}</dd><dt>失败</dt><dd>{item.failed_executions}</dd></dl></article>)}</div>;
}
