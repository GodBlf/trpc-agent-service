# 01: Shared Channel Configuration And Management

**What to build:** Tenant operators can create and manage Enterprise WeChat and Telegram Channel Bindings from the Management Console. Provider selection, webhook settings, enable/disable state, write-only secrets, authorization, tenant isolation, request ID propagation, and a common provider contract are available end to end.

**Blocked by:** Stage 3: Stage 3 Gate And Handoff; Stage 3.5: Framework Runtime Handoff

**Status:** ready-for-agent

- [ ] Authorized roles can create, inspect, update, enable, disable, and delete tenant-owned Channel Bindings for the two supported providers.
- [ ] Secrets are write-only and never appear in logs, traces, errors, API responses, or rendered UI.
- [ ] Cross-tenant reads and mutations are rejected consistently by API and UI.
- [ ] Binding state, webhook information, latest delivery status, and stable provider configuration errors are exposed through the frontend and API.
- [ ] The shared provider contract preserves tenant, user, session, request, retry, and delivery identity from inbound callback through outbound reply.
