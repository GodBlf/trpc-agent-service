# 04: Cross-Provider Delivery Reliability And Replay

**What to build:** Operators can inspect and replay WeCom Smart Bot frames and Telegram long-polling updates from the Management Console while both platform-owned Provider Accounts share consistent idempotency, retry, limits, cancellation, failure persistence, connection health, and request correlation behaviour.

**Blocked by:** 02: Enterprise WeChat Protocol Adapter And Local Closed Loop; 03: Telegram Protocol Adapter And Local Closed Loop

**Status:** ready-for-agent

- [ ] Both providers expose stable accepted, retried, rejected, and terminal-failed delivery outcomes.
- [ ] Duplicate delivery, retry exhaustion, timeout, cancellation, rate-limit, length-limit, and attachment-limit scenarios are deterministic and tenant-scoped.
- [ ] Protocol replay is repeatable and does not require external provider credentials.
- [ ] Unmapped, ambiguous, duplicate, disabled, disconnected, and provider-unavailable statuses are visible without exposing secrets.
- [ ] Latest delivery status and bounded failure details are visible to authorized operators without exposing secrets or unrelated tenant data.
- [ ] API contracts and desktop/mobile Playwright flows cover provider selection, replay, status, and failure recovery.
