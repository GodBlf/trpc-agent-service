# 02: Enterprise WeChat Protocol Adapter And Local Closed Loop

**What to build:** An Enterprise WeChat webhook can be verified and converted into a tenant-scoped platform request, routed through the existing Runner, and converted back into an Enterprise WeChat reply. Deterministic protocol fixtures provide a complete local callback-to-reply loop without requiring live credentials.

**Blocked by:** 01: Shared Channel Configuration And Management

**Status:** ready-for-agent

- [ ] Valid Enterprise WeChat callbacks pass signature verification and invalid or expired callbacks are rejected with stable public errors.
- [ ] Text callbacks map deterministically to the correct tenant, user, conversation, and Session; duplicate and out-of-order deliveries are handled safely.
- [ ] Agent events are converted to Enterprise WeChat text replies with asynchronous delivery semantics.
- [ ] Minimal supported media messages are parsed or rejected with an explicit, bounded limitation.
- [ ] Timeout, retry, rate-limit, message-length, cancellation, and terminal-failure paths are covered by deterministic protocol replay tests.
- [ ] End-to-end tests prove callback to Agent execution to Enterprise WeChat reply while preserving request and tenant identity.
