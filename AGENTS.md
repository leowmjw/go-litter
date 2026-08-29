# AGENTS

## Language

- Use latest Go v1.27.x and its full capabilities
- Always prefer stdlib if available and it make sense

## Orchestration

- Use Temporal + Go SDK to handle the workflows
- Use Temporal testsuite as much as possible to have widest test coverage
- Use advanced techique like registerdelayedcallback to simulate long running system
# - Ensure use Temporal Worker version to ensure multiple workflow versions can co-exists

## Runtime

- Whole system should be fully testable standalone with Go binary
- Use modern Go capabilities (when needed): generics, structured log, built-in http
routing, testing/synctest
- Prefer concrete structs with first-class function fields and anonymous function method replacement; avoid Go interfaces unless a dependency or protocol boundary truly requires one
- Use function replacement and `testing/synctest` to keep behavior deterministic

## Testing

- All new cases should be at least 80% coverage
- Unit tests and inetgration tests MUST be comepleted without needing to spin up any
 external dependencies
- Only Full End-to-End should need to stand up Temporal Test Server

## Tools

- Use mise to run tasks, set env variables, automate
- Setup env like CGO_ENABLED=0, PATH append to use ~/go/bin and loading from .env
- Tools available: ripgrep, fzf, air, goreleaser, watchexec
- Use overmind to start temporal-cli + air
- Each session has its own `Procfile.<session>` at workspace root and its own
  `.air.toml` under `cmd/<session>/`
- Use `overmind start -f Procfile.<session>` via `mise run <session>:dev`

## Specification (MVP)

- Follow `PRD.md` for product behavior and `TECHSPEC.md` for implementation choices and milestone order; neither overrides this file
- `EXAMPLE/` is the canonical Java reference imported from `redplanetlabs/rama-demo-gallery`; use its `README.md`, `src/main/java/`, and `src/test/java/`
- Port the six gallery modules in the session order defined by PRD.md, starting with `cmd/session-1`
- Once all gallery examples are complete, implement `cmd/quick-tutorial` from the six-page Rama tutorial beginning at https://redplanetlabs.com/docs/~/tutorial1.html
- Ask if anything is unsure or contradictory
- Use mise to launch the workshops for all sessions

## Specification (Advanced)
- Finally CI/CD will use End-to-End Tests
- Implement this ONLY after Unit/Integration tests are passing

## Implementation Status & Learnings

<TODO>

### Overmind + air per-session (example)  convention

- **One Procfile per session** at workspace root: `Procfile.session-1`, `Procfile.session-2`, …
- **Three standard processes** per session Procfile:
  - `temporal`: `temporal server start-dev --db-filename .temporal/temporal.db --metrics-port 8077`
  - `app`: `air -c cmd/session-N/.air.toml`
- **One `.air.toml` per session** at `cmd/session-N/.air.toml`; build output goes to `.air-session-N/`
- **mise task** `session-N:dev` runs `overmind start -f Procfile.session-N`
- `.temporal/` and `.air-session-*/` are git-ignored



