# Offline import from a Portfolio workspace backup

## Purpose and flow

This import is a one-time, operator-run bridge from the Portfolio `business-suite/v1` workspace export into an already initialized Dining repository database. It is separate from the repository's same-organization backup/restore behavior. The operator stops Dining writers, makes a verified full-file snapshot, inspects the source file, explicitly confirms the source organization, and maps that organization to an existing Dining organization. The importer then validates the Dining-only candidate and inserts it in one SQLite transaction. A preview reports counts and validation findings without writing.

The source envelope is the existing Portfolio format (`format`, `organizationId`, `exportedAt`, and `tables`). The input may contain the other stations' business tables because Portfolio exports the whole workspace. This importer recognizes those names only to ignore their payload; it reads, validates, maps, and writes only the allowlisted `dining_*` tables. Unknown Dining table names, malformed envelopes, nonempty authentication data, or source rows from another organization fail closed. Existing destination records are never overwritten: any ID, natural-key, or tenant conflict rejects the complete import.

## Mapping and relationship rules

- The operator supplies an explicit source-organization confirmation and an existing destination organization ID. Every imported row's `organizationId` is rewritten to that destination.
- Dining business rows have no user/account identity columns in the Portfolio identity-mapping model. Users, grants, sessions, organizations, audit events, and idempotency records are never imported or remapped.
- Dining primary IDs and internal foreign-key values are preserved. Before writing, validate every Dining table's exact column set and scalar types, duplicate IDs, tenant ownership, supported business invariants, and all Dining references against the source candidate. Cross-organization or missing links reject the import.
- The active table-session `sessionToken` is a guest ordering capability. Replace it with a fresh token during import, preserve the session row ID and order references, and return the token rotation count; imported QR/guest links must be reissued. Closed sessions also receive a fresh token so no source capability crosses the boundary.
- Require the complete current Dining table manifest. Portfolio v1's populated legacy fixture has nine Dining tables; the other current Dining tables are represented by empty arrays in a complete export. Do not infer that absent keys mean empty data.

## Safety boundary

- The operation is offline only. It accepts local file paths, exposes no HTTP endpoint, performs no network request, and is never run automatically at server startup or deployment.
- It must target an existing regular Dining database with the repository schema marker and a pre-existing destination organization. It must not call the server's migration-capable `openDB` function or create a database.
- The operator must stop the service and all other database writers first. A process lock can prevent overlapping importer runs but cannot prove that unrelated SQLite writers are stopped.
- Verify the source and destination paths are distinct regular files; cap input bytes and rows; parse the source without mutating it; and create a consistent pre-import snapshot at a new operator-selected path before beginning the destination transaction. Retain that snapshot on validation, write, or verification failure.
- Insert only into Dining tables in dependency order, in a single transaction. Verify per-table row counts and foreign-key/invariant checks before commit. On any error, rollback all Dining writes; preserve the source, destination identities, and non-Dining destination data.
- This imports a copy; it does not reconcile concurrent edits, merge changes, delete source data, or claim a live cutover. Before directing production traffic to this database, operators must separately compare source/destination counts and business totals and schedule a final write freeze.

## Acceptance plan

First test parsing and mapping against a sanitized test envelope derived from Portfolio's checked-in legacy fixture. Verify the imported Dining rows and relationships match after tenant remapping and token rotation, while non-Dining fixture rows and destination identity tables remain untouched. Cover missing/unknown tables, invalid types, cross-tenant rows, broken references, duplicate IDs, destination conflicts, bad paths, missing schema marker, invalid organization mapping, snapshot retention, and all-or-nothing rollback. Only after those tests pass should the offline command be wired; it remains unexercised against production data.
