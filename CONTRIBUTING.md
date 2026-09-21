# Contributing to Raildrop

Thanks for your interest in contributing! This document covers everything you need
to go from clone to merged pull request.

## Development setup

Requirements: Node 20+, pnpm 11 (Corepack handles this automatically).

```bash
corepack enable
pnpm install
pnpm build      # tsup — ESM + CJS + d.ts for every entry point
pnpm test       # node:test suite
pnpm lint       # eslint
pnpm typecheck  # tsc --noEmit
pnpm test:bundle  # verifies the client bundle stays dependency-free
pnpm test:package # builds and verifies the publishable package shape
```

## Project conventions

- **TypeScript strict mode.** No `any`. No non-null assertions (`!`). Prefix unused
  variables with `_`.
- **Nullish coalescing.** Use `??` over `||`.
- **Arrow-function constants** for new helpers: `const thing = () => {}` over
  `function thing() {}`, unless hoisting requires otherwise.
- **Explicit type imports**: `import type { Foo } from './foo'`.
- **Prettier**: 100 print width, single quotes, es5 trailing commas, 2-space tabs.
- **No comments** unless explaining a non-obvious invariant.
- Entry points must stay dependency-light: `raildrop/client` and `raildrop/expo`
  must not grow runtime dependencies (enforced by `pnpm test:bundle`).
- Never log or persist bucket credentials, `RAILDROP_SECRET`, session tokens,
  or signed URLs.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <subject>
```

- Types: `feat`, `fix`, `refactor`, `docs`, `style`, `chore`, `test`
- Subject: imperative mood, lowercase, no period, max 50 characters
- Example: `feat(client): add per-file progress events`

## Pull requests

1. Fork / branch from `main`.
2. Make your change with tests. Bug fixes need a regression test; features need
   coverage for the happy path and failure paths.
3. Run `pnpm lint && pnpm typecheck && pnpm test && pnpm test:bundle`.
4. **Add a changeset** for any user-facing change:

   ```bash
   pnpm changeset
   ```

   Choose a semver level: `patch` for fixes, `minor` for new features,
   `major` for breaking changes.
5. Update the README if you changed public behavior.
6. Open the PR with a clear description of what and why.

CI runs lint, typecheck, tests, and the bundle guard on Node 20 and 22.
All checks must pass before merge.

## Release process

Releases are automated with [Changesets](https://github.com/changesets/changesets):

1. Every PR with a changeset feeds a "Version Packages" PR that bumps the version
   and updates `CHANGELOG.md`.
2. Merging that PR to `main` publishes to npm via GitHub Actions using
   npm Trusted Publishing (OIDC) — no tokens are stored.
3. A GitHub Release with changelog notes is created automatically.

Maintainers: to enable publishing, configure a Trusted Publisher on npmjs.com for
the `raildrop` package pointing at this repository and the `release.yml` workflow.

## Reporting issues

- Bug reports: use the bug report template and include a minimal reproduction.
- Feature requests: describe the problem first, then your proposed API.
- Security issues: do **not** open a public issue — see [SECURITY.md](./SECURITY.md).

## Code of conduct

By participating you agree to the [Code of Conduct](./CODE_OF_CONDUCT.md).
