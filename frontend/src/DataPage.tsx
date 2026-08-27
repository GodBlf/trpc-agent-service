import { useEffect, useState } from "react";
import { Database, RefreshCw } from "lucide-react";
import { api, type BackendHealth, type MigrationResult, type SessionEvent, type SessionState, type MemoryRecord } from "./api";
import { AsyncState } from "./AsyncState";

export function DataPage() {
  const [backend, setBackend] = useState<BackendHealth>();
  const [sessionID, setSessionID] = useState("demo-session");
  const [session, setSession] = useState<SessionState>();
  const [events, setEvents] = useState<SessionEvent[]>([]);
  const [memory, setMemory] = useState<MemoryRecord[]>([]);
  const [migration, setMigration] = useState<MigrationResult>();
  const [failed, setFailed] = useState(false);
  const load = async () => { try { setFailed(false); const [b, s, e, m] = await Promise.all([api.backend(), api.session(sessionID).catch(() => undefined), api.sessionEvents(sessionID), api.memory(sessionID)]); setBackend(b.health); setSession(s); setEvents(e.items); setMemory(m.items); } catch { setFailed(true); } };
  useEffect(() => { void load(); }, [sessionID]);
  if (failed) return <AsyncState kind="error" retry={() => void load()} />;
  if (!backend) return <AsyncState kind="loading" />;
  return <div className="data-page">
    <div className="data-toolbar"><div><strong>数据后端</strong><span className={`status ${backend.status}`}>{backend.status}</span><small>{backend.backend}</small></div><button className="icon-button" title="刷新" onClick={() => void load()}><RefreshCw aria-hidden="true" /></button></div>
    <div className="data-controls"><label>Session ID<input value={sessionID} onChange={e => setSessionID(e.target.value)} /></label><button className="primary" onClick={async () => setMigration(await api.migrate(true))}><Database aria-hidden="true" />Dry-run 迁移</button></div>
    {migration && <div className="inline-error">迁移 {migration.status}：{migration.message}</div>}
    <div className="data-grid"><section className="runtime-item"><h3>Session / Summary</h3>{session ? <dl><dt>事件数</dt><dd>{session.event_count}</dd><dt>序列</dt><dd>{session.sequence}</dd><dt>Summary</dt><dd>{session.summary || "暂无"}</dd></dl> : <AsyncState kind="empty" />}</section><section className="runtime-item"><h3>Session Events</h3>{events.length ? <ol className="event-list">{events.map(e => <li key={e.id}><code>#{e.sequence}</code> {e.type}<span>{e.payload}</span></li>)}</ol> : <AsyncState kind="empty" />}</section><section className="runtime-item"><h3>Memory</h3>{memory.length ? <dl>{memory.map(item => <div key={item.id}><dt>{item.key}</dt><dd>{item.value}</dd></div>)}</dl> : <AsyncState kind="empty" />}</section></div>
  </div>;
}
