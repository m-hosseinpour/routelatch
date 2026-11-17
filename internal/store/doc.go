// Package store is RouteLatch's single-writer persistence layer. It implements
// contract.Store for state.json and config.json, owns the process instance lock
// and exposes the XDG paths every other component uses (docs/contracts.md §4).
//
// The rules it enforces:
//
//   - Directories are created 0700 and files 0600; a state directory or file
//     that is group/other accessible fails closed instead of being repaired
//     silently.
//   - One writer. Mutate and MutateConfig hold an advisory lock for the whole
//     read-modify-write. A second holder gets contract.StateLocked.
//   - Atomic replace: same-directory temp file, O_EXCL, fsync, rename, then
//     directory fsync. A failed rename leaves the old file and removes the
//     temp file.
//   - Corrupt or newer-schema state fails closed and is never overwritten.
//     Older schema is migrated in memory, backed up first, then written.
//   - Config and state are separate files; there is no promise that both
//     change together.
package store
