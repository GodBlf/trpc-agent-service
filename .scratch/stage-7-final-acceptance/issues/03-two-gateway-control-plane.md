# 03: Two-Gateway Shared Control Plane

**What to build:** An operator can create or publish configuration through one
Gateway and immediately query and execute it through another Gateway; restarting
either Gateway does not lose or diverge the authoritative configuration.

**Blocked by:** 01: Single Gateway Durable Restart.

**Status:** resolved

- [x] Production and Compose use PostgreSQL as the shared Control Plane Store
  for Tenant, Agent App, Deployment, Deployment Version, Backend Selection,
  Channel routing, Governance Policy, and idempotency state.
- [x] A black-box test creates and publishes through Gateway A, reads and routes
  through Gateway B, restarts a Gateway, and repeats the workflow successfully.
- [x] Gateway instances do not rely on process-local correctness caches.
- [x] Control-plane failure returns `control_plane_unavailable`; there is no
  stale or InMemory fallback.
- [x] Concurrent lifecycle and idempotency invariants remain atomic across the
  two Gateway instances.
- [x] The ticket documents and runs its own two-Gateway acceptance command.

## Comments

已由 `./scripts/stage7-compose-acceptance.sh` 和总门禁验证。
