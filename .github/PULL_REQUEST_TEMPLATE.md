## Summary

What changes, and why?

## Checks

- [ ] `go build ./...`
- [ ] `go vet ./...`
- [ ] `go test -count=1 ./...`
- [ ] `go test -count=1 -v ./internal/acceptance/` (208 PASS / 0 FAIL / 13 NOTE expected)
- [ ] Documentation updated where the change makes it stale

## Scope and risk

- [ ] No real share links, subscription URLs, credentials or private paths are
      included.
- [ ] Behaviour changes to routing, fail-closed guarantees, the CLI/JSON
      contract, state or permissions are covered by tests and were reviewed
      independently.
- [ ] Unverified behaviour or skipped checks are listed below rather than
      described as passing.
