# 03: Telegram Protocol Adapter And Local Closed Loop

**What to build:** A Telegram webhook update can be authenticated and converted into a tenant-scoped platform request, routed through the existing Runner, and converted back into a Telegram reply. Deterministic update fixtures provide a complete local update-to-reply loop without requiring live credentials.

**Blocked by:** 01: Shared Channel Configuration And Management

**Status:** ready-for-agent

- [ ] Telegram updates pass webhook authentication and malformed or unauthorized updates are rejected with stable public errors.
- [ ] Private-chat and group-chat updates map deterministically to the correct tenant, user, conversation, and Session; duplicate updates are suppressed.
- [ ] Agent events are converted to asynchronous Telegram text replies.
- [ ] Message-length limits and minimal supported media messages are handled explicitly.
- [ ] Timeout, retry, rate-limit, cancellation, and terminal-failure paths are covered by deterministic protocol replay tests.
- [ ] End-to-end tests prove update to Agent execution to Telegram reply while preserving request and tenant identity.
