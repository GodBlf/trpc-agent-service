import { useEffect, useState } from "react";
import { Boxes, Building2, ChevronDown, Database, LayoutDashboard, Network, Rocket, ServerCog } from "lucide-react";
import { api, type Identity } from "./api";
import { AsyncState } from "./AsyncState";
import { TenantsPage } from "./TenantsPage";
import { AppsPage } from "./AppsPage";
import { DeploymentsPage } from "./DeploymentsPage";
import { RuntimePage } from "./RuntimePage";
import { DataPage } from "./DataPage";

const navigation = [
  { label: "概览", icon: LayoutDashboard },
  { label: "租户", icon: Building2 },
  { label: "Agent 应用", icon: Boxes },
  { label: "部署", icon: Rocket },
  { label: "运行节点", icon: Network },
  { label: "数据管理", icon: Database },
];

export default function App() {
  const [identity, setIdentity] = useState<Identity>();
  const [failed, setFailed] = useState(false);
  const [active, setActive] = useState("概览");

  const load = () => {
    setFailed(false);
    api.identity().then(setIdentity).catch(() => setFailed(true));
  };
  useEffect(load, []);

  const switchTenant = async (tenantID: string) => {
    try {
      setIdentity(await api.switchTenant(tenantID));
    } catch {
      setFailed(true);
    }
  };

  if (failed) return <main className="centered"><AsyncState kind="error" retry={load} /></main>;
  if (!identity) return <main className="centered"><AsyncState kind="loading" /></main>;

  return (
    <div className="app-shell">
      <aside>
        <div className="brand"><ServerCog aria-hidden="true" /><span>Agent Platform</span></div>
        <nav aria-label="主导航">
          {navigation.map(({ label, icon: Icon }) => (
            <button key={label} className={active === label ? "active" : ""} onClick={() => setActive(label)}>
              <Icon aria-hidden="true" /><span>{label}</span>
            </button>
          ))}
        </nav>
      </aside>
      <section className="workspace">
        <header>
          <div><span className="eyebrow">管理控制台</span><h1>{active}</h1></div>
          <label className="tenant-switcher">
            <span>当前租户</span>
            <div>
              <select value={identity.active_tenant_id} onChange={(event) => void switchTenant(event.target.value)}>
                {identity.assignments.map((assignment) => <option value={assignment.tenant_id} key={assignment.tenant_id}>{assignment.tenant_name}</option>)}
              </select>
              <ChevronDown aria-hidden="true" />
            </div>
          </label>
          <div className="identity"><span>{identity.name}</span><small>{identity.active_role}</small></div>
        </header>
        <main>
          <div className="page-heading"><div><h2>{active}</h2><p>由后端提供的实时平台数据</p></div></div>
          {active === "租户" ? <TenantsPage identity={identity} identityChanged={load} /> : active === "Agent 应用" ? <AppsPage identity={identity} /> : active === "部署" ? <DeploymentsPage identity={identity} /> : active === "运行节点" ? <RuntimePage /> : active === "数据管理" ? <DataPage identity={identity} /> : <AsyncState kind="empty" />}
        </main>
      </section>
    </div>
  );
}
