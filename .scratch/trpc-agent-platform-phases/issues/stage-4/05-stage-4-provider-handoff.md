# 05: Stage 4 Provider Integration Acceptance And Handoff

**What to build:** The completed WeCom Smart Bot and Telegram Bot provider matrix, Provider Account and Bot Tenant Allowlist contracts, environment configuration, connection lifecycle, protocol limitations, replay commands, and smoke-test reporting are packaged for Stage 5 consumption.

**Blocked by:** 04: Cross-Provider Delivery Reliability And Replay

**Status:** ready-for-agent

- [ ] Enterprise WeChat and Telegram protocol, signature, replay, duplicate, isolation, retry, limit, authorization, API, and Playwright acceptance suites pass.
- [ ] Credential smoke tests are supported when credentials are available and are reported as unavailable, never passed, when they are not.
- [ ] Real provider secrets never appear in logs, traces, errors, API responses, or the DOM.
- [ ] Handoff explicitly states that WeCom self-built applications and their CorpID/AgentID/application Secret/Access Token credentials are not used.
- [ ] Stage 4 acceptance, known limitations, reproduction commands, and Stage 5 handoff contracts are documented.
- [ ] The handoff freezes provider capabilities, Channel Binding operations, request ID propagation, and platform-specific constraints.
