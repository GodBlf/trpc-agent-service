# 01: Platform Provider Accounts And Bot Tenant Allowlist

**What to build:** Platform administrators can configure the two platform-owned Provider Accounts and maintain a server-owned Bot Tenant Allowlist. The Management Console shows provider connection state, enables or disables routing, and maps an external IM subject to one Tenant and Agent App. Traditional WeCom self-built applications are explicitly excluded.

**Blocked by:** Stage 3: Stage 3 Gate And Handoff; Stage 3.5: Framework Runtime Handoff

**Status:** ready-for-agent

- [ ] Server configuration reads `TRPC_TELEGRAM_BOT_USERNAME`, `TRPC_TELEGRAM_BOT_TOKEN`, `TRPC_WECOM_BOT_ID`, and `TRPC_WECOM_BOT_SECRET` without exposing values.
- [ ] No WeCom self-built-app credentials or traditional application webhook configuration is accepted or required.
- [ ] Platform administrators can create, update, disable, and remove allowlist entries; conflicting mappings are rejected atomically.
- [ ] Tenant users can inspect only mappings and provider status relevant to their Tenant.
- [ ] Provider Account status distinguishes unconfigured, connecting, connected, disconnected, and stopping.
- [ ] The shared provider contract preserves Tenant, Agent App, external subject, user, Session, request, retry, and delivery identity.
