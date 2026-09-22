# raildrop

## 0.2.0

### Minor Changes

- [#1](https://github.com/akpor-kofi/raildrop/pull/1) [`b3cfa4f`](https://github.com/akpor-kofi/raildrop/commit/b3cfa4f3cca87dc2d89af1f3bc0e7344444ec146) Thanks [@akpor-kofi](https://github.com/akpor-kofi)! - Adds the Raildrop Go SDK under `sdk/go` (module `github.com/akpor-kofi/raildrop/sdk/go`):
  typed route builders, the `prepare`/`finalize`/`abort` HTTP handler as a standard
  `http.Handler`, a stdlib-only S3 SigV4 storage implementation, the public gateway,
  cleanup/global-asset maintenance, a server-to-server upload client, a Fiber adapter
  (`sdk/go/fiberadapter`), a `raildrop` CLI, and a testing harness. Session tokens and the
  wire protocol are byte-compatible with the TypeScript SDK, locked by cross-language
  conformance fixtures run in both test suites.
