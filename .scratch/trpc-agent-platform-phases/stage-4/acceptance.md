# Stage 4 Acceptance

Status: in progress

Stage 4 adds a WeCom API-mode Smart Bot WebSocket adapter and a Telegram long-polling Bot adapter behind the Stage 3 ChannelAdapter contract. Neither path uses a traditional WeCom self-built application. Both providers support deterministic local protocol replay without external credentials.

## Automated Gate

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `cd frontend && npm run typecheck && npm test && npm run build`

The provider tests cover authentication, callback/update parsing, tenant-scoped binding selection, duplicate suppression, provider sequence ordering, bounded text limits, explicit unsupported-media outcomes, and callback-to-Runner-to-reply routing. Channel Binding secrets are write-only for real providers. Credential smoke tests are optional and must be reported as unavailable when credentials are absent.

## Known Limitations

Telegram inbound long polling, Provider Account status, Bot Tenant Allowlist routing, and service-owned transport shutdown are implemented. Telegram `sendMessage`, the official WeCom Smart Bot authentication/acknowledgement/`aibot_respond_msg` frame protocol, real-provider reply correlation, persisted allowlist configuration, and credential smoke tests remain open. Live provider credentials from `.env.local` must only be used by explicit smoke tests. Complex media processing is outside this phase.
