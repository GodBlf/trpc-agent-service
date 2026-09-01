# Stage 4 Acceptance

Stage 4 adds Enterprise WeChat and Telegram Channel Adapters behind the Stage 3 ChannelAdapter contract. Both providers support deterministic local protocol replay without external credentials.

## Automated Gate

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `cd frontend && npm run typecheck && npm test && npm run build`

The provider tests cover authentication, callback/update parsing, tenant-scoped binding selection, duplicate suppression, provider sequence ordering, bounded text limits, explicit unsupported-media outcomes, and callback-to-Runner-to-reply routing. Channel Binding secrets are write-only for real providers. Credential smoke tests are optional and must be reported as unavailable when credentials are absent.

## Known Limitations

Enterprise WeChat and Telegram outbound delivery use an injectable sender seam for deterministic local acceptance. Live provider API credentials and external network smoke tests are not required for the automated gate. Enterprise WeChat non-text callbacks are explicitly rejected as unsupported media in this phase.
