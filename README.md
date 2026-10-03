# Wayseer module: file

The `file` module of Wayseer Desktop: entities, links, series and events read from CSV and JSON
files through a mapping in its options, reloaded when the files change. Recordings can be
replayed so their newest sample lands now.

```
go get wayseer.dev/modules/file
```

Wayseer links it in, so users do not install it. Its options and what it shows are in Wayseer's
user guide, under "Files".

`testdata` holds a small made-up fleet: hosts, services, CPU samples and events, a malformed
CSV, and `conformance.yaml`, the options the conformance suite runs with.

## Working on it

```
make check   # what CI runs: tests with the conformance suite, vet and lint for every platform, a key scan
make help    # every target
```

It imports only the SDK (`wayseer.dev/sdk`), the standard library and its own
dependencies; `TestImportsOnlyTheSDK` keeps it that way. golangci-lint is pinned in
`tools/go.mod`, and gitleaks runs at a pinned version through `go run`.

To change it alongside the SDK or the app, use a Go workspace: `go.work` here with
`use . ../../sdk`, or the app's `make workspace`, which writes one for the whole of Wayseer
Desktop. `go.work` is ignored by git.

## Licence

MIT; see `LICENSE`.
