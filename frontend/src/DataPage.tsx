import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Database, RefreshCw } from "lucide-react";
import { api, type BackendHealth, type Identity, type MemoryRecord, type MigrationResult, type SessionEvent, type SessionState } from "./api";
import { AsyncState } from "./AsyncState";

export function DataPage({ identity }: { identity: Identity }) {
  const [backend, setBackend] = useState<BackendHealth>();
  const [backendChoice, setBackendChoice] = useState("inmemory");
  const [backendAddress, setBackendAddress] = useState("");
  const [sessionID, setSessionID] = useState("demo-session");
  const [session, setSession] = useState<SessionState>();
  const [events, setEvents] = useState<SessionEvent[]>([]);
  const [memory, setMemory] = useState<MemoryRecord[]>([]);
  const [migration, setMigration] = useState<MigrationResult>();
  const [failed, setFailed] = useState(false);

  const load = useCallback(async () => {
    try {
      setFailed(false);
      const [backendResponse, state, eventResponse, memoryResponse] = await Promise.all([
        api.backend(), api.session(sessionID).catch(() => undefined),
        api.sessionEvents(sessionID), api.memory(sessionID),
      ]);
      setBackend(backendResponse.health); setBackendChoice(backendResponse.backend);
      setSession(state); setEvents(eventResponse.items); setMemory(memoryResponse.items);
    } catch { setFailed(true); }
  }, [sessionID]);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    if (!migration || migration.status !== "running") return;
    const timer = window.setInterval(async () => {
      try { setMigration(await api.migration(migration.id)); } catch { setFailed(true); }
    }, 500);
    return () => window.clearInterval(timer);
  }, [migration]);

  const selectBackend = async () => {
    try { setBackend((await api.selectBackend(backendChoice, backendAddress || undefined)).health); }
    catch { setFailed(true); }
  };
  const createMigration = async () => {
    try {
      setMigration(await api.migrate({ dry_run: true, source_address: backendAddress || "127.0.0.1:6379", destination_path: "data/stage2.db", checkpoint_path: "data/stage2-migration.checkpoint", batch_size: 100 }));
    } catch { setFailed(true); }
  };

  if (failed) return <AsyncState kind="error" retry={() => void load()} />;
  if (!backend) return <AsyncState kind="loading" />;
  const canConfigure = identity.active_role === "platform_admin" || identity.active_role === "tenant_admin";
  return <div className="data-page">
    <div className="data-toolbar"><div><strong>数据后端</strong><span className={`status ${backend.status}`}>{backend.status}</span><small>{backend.backend}</small></div><button className="icon-button" title="刷新" onClick={() => void load()}><RefreshCw aria-hidden="true" /></button></div>
    <div className="data-controls">
      <label>后端<select value={backendChoice} onChange={(event) => setBackendChoice(event.target.value)}><option value="inmemory">InMemory</option><option value="redis">Redis</option><option value="sqlite">SQLite</option></select></label>
      <label>地址或路径<input value={backendAddress} onChange={(event) => setBackendAddress(event.target.value)} placeholder="Redis 地址或 SQLite 路径" /></label>
      {canConfigure && <button onClick={() => void selectBackend()}>应用后端</button>}
      <label>Session ID<input value={sessionID} onChange={(event) => setSessionID(event.target.value)} /></label>
      {canConfigure && <button className="primary" onClick={() => void createMigration()}><Database aria-hidden="true" />Dry-run 迁移</button>}
    </div>
    {migration && <div className={`migration-state ${migration.status}`}>迁移 {migration.status}：{migration.source_count} / {migration.destination_count} {migration.message}</div>}
    <div className="data-grid">
      <DataPanel title="Session / Summary">{session ? <dl><dt>事件数</dt><dd>{session.event_count}</dd><dt>序列</dt><dd>{session.sequence}</dd><dt>Summary</dt><dd>{session.summary || "暂无"}</dd></dl> : <AsyncState kind="empty" />}</DataPanel>
      <DataPanel title="Session Events">{events.length ? <ol className="event-list">{events.map((event) => <li key={event.id}><code>#{event.sequence}</code> {event.type}<span>{event.payload}</span></li>)}</ol> : <AsyncState kind="empty" />}</DataPanel>
      <DataPanel title="Memory">{memory.length ? <dl>{memory.map((item) => <div key={item.id}><dt>{item.key}</dt><dd>{item.value}</dd></div>)}</dl> : <AsyncState kind="empty" />}</DataPanel>
    </div>
  </div>;
}

function DataPanel({ title, children }: { title: string; children: ReactNode }) { return <section className="runtime-item"><h3>{title}</h3>{children}</section>; }
