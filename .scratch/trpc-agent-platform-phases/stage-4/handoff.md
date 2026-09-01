# Stage 4 Handoff

Stage 4 freezes the following contracts for Stage 5:

- Provider names are `enterprise_wechat` and `telegram`; `mock` remains a local validation provider.
- `ChannelCoordinator` dispatches providers through the existing `ChannelAdapter` interface and keeps binding selection tenant-scoped.
- Real-provider secrets are accepted only during binding creation/replacement and are omitted from list and response payloads.
- Enterprise WeChat callbacks use SHA-1 token/timestamp/nonce verification when query metadata is present, with deterministic HMAC fixture verification for local replay. Telegram callbacks use `X-Telegram-Bot-Api-Secret-Token`.
- Provider message IDs drive duplicate suppression and provider sequence values drive out-of-order rejection.
- Stable public outcomes include signature invalid, callback invalid, attachment rejected, message too long, timeout, rate limited, disabled, unavailable, and retry exhaustion.
- Web UI and deterministic replay validate the flow but do not count toward the two real providers required by the README.
