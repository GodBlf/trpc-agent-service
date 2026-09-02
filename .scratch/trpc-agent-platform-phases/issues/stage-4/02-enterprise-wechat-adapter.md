# 02: WeCom Smart Bot WebSocket Adapter And Local Closed Loop

**What to build:** The WeCom API-mode Smart Bot connects with BotID and long-connection Secret, receives `aibot_msg_callback` frames over WebSocket, routes allowlisted subjects through the existing Runner, and sends `aibot_respond_msg` replies. Deterministic frames provide a local closed loop without live credentials.

**Blocked by:** 01: Shared Channel Configuration And Management

**Status:** ready-for-agent

- [ ] WebSocket authentication and Smart Bot frame parsing use BotID/long-connection Secret; traditional self-built-app webhook verification is absent.
- [ ] Allowlisted external subjects map deterministically to Tenant, Agent App, user, conversation, and stable Session; unmapped and conflicting subjects never invoke Agent.
- [ ] Agent replies are encoded as Smart Bot response frames with asynchronous delivery and bounded reconnect/backoff.
- [ ] Text is required for the Stage 4 closed loop; unsupported media returns a stable bounded status.
- [ ] Duplicate callback IDs, cancellation, timeout, retry, disconnect, and shutdown paths are deterministic and leak-free.
- [ ] Frame replay tests prove callback to Agent execution to Smart Bot reply while preserving request and tenant identity.
