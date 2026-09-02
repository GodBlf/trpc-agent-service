# Stage 4 IM Providers

Stage 4 uses two platform-owned Bot accounts:

- Telegram Bot API with long polling.
- WeCom API-mode Smart Bot with a WebSocket long connection.

WeCom self-built applications are not used. Do not configure CorpID, AgentID, an application Secret, an application Access Token, EncodingAESKey, or an application callback URL.

## Local Configuration

Create an ignored `.env.local` file containing the required process environment variables:

```bash
TRPC_TELEGRAM_BOT_USERNAME=...
TRPC_TELEGRAM_BOT_TOKEN=...
TRPC_WECOM_BOT_ID=...
TRPC_WECOM_BOT_SECRET=...
```

`./start.sh` loads this file before starting the Go service. The values are never returned by the management API. Missing credentials leave the corresponding provider in `unconfigured` state without preventing the HTTP service from starting.

The Management Console exposes Provider Account connection status and Bot Tenant Allowlist routes. Only a platform administrator can create a route from an external conversation subject to a Tenant and Agent App. Unmapped messages do not invoke the Agent.

## Acceptance Boundary

CI uses deterministic transport and protocol tests without credentials. Real-provider smoke tests are explicit local operations. The Web UI remains an IM Simulator and does not count as a real provider.

Text is the required end-to-end message format. Complex media download, conversion, OCR, and storage remain outside Stage 4.
