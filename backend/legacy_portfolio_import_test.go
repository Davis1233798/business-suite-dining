package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func portfolioDiningFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "portfolio_dining_backup_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParsePortfolioV1DiningOnlyAndExplicitOrganizationMap(t *testing.T) {
	candidate, err := parseLegacyPortfolioDiningBackup(portfolioDiningFixture(t), "org_go_fixture", "org_dining_target")
	if err != nil {
		t.Fatal(err)
	}
	if candidate.SourceOrganization != "org_go_fixture" || candidate.DestinationOrganization != "org_dining_target" {
		t.Fatalf("organization mapping = %q -> %q", candidate.SourceOrganization, candidate.DestinationOrganization)
	}
	if candidate.RowCount != 21 {
		t.Fatalf("Dining rows = %d, want 21", candidate.RowCount)
	}
	if candidate.IgnoredTables["pos_sales"] != 1 {
		t.Fatalf("non-Dining rows not reported as ignored: %#v", candidate.IgnoredTables)
	}
	if len(candidate.Tables) != len(legacyDiningImportTables) {
		t.Fatalf("selected tables = %d, want %d", len(candidate.Tables), len(legacyDiningImportTables))
	}
}

func TestParsePortfolioDiningRejectsIncompleteOrUnsafeInput(t *testing.T) {
	tests := []struct {
		name   string
		change func(map[string]any)
	}{
		{"source confirmation mismatch", func(v map[string]any) { v["organizationId"] = "org_other" }},
		{"missing dining table", func(v map[string]any) { delete(v["tables"].(map[string]any), "dining_payments") }},
		{"unknown dining table", func(v map[string]any) { v["tables"].(map[string]any)["dining_secret_records"] = []any{} }},
		{"unknown foreign table", func(v map[string]any) { v["tables"].(map[string]any)["private_users"] = []any{} }},
		{"nonempty authentication rows", func(v map[string]any) {
			v["tables"].(map[string]any)["station_accounts"] = []any{map[string]any{"email": "must-not-import@example.test"}}
		}},
		{"cross-tenant dining row", func(v map[string]any) {
			rows := v["tables"].(map[string]any)["dining_locations"].([]any)
			rows[0].(map[string]any)["organizationId"] = "org_other"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(portfolioDiningFixture(t), &value); err != nil {
				t.Fatal(err)
			}
			tc.change(value)
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = parseLegacyPortfolioDiningBackup(data, "org_go_fixture", "org_dining_target"); err == nil {
				t.Fatal("unsafe or incomplete backup accepted")
			}
		})
	}
}

func TestInspectLegacyPortfolioDiningIsReadOnlyAndChecksDestinationSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "dining.sqlite")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO organizations(id,name,slug) VALUES(?,?,?)", "org_dining_target", "Target", "org_dining_target"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	before, err := diningCounts(db)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	plan, err := inspectLegacyPortfolioDining(db, portfolioDiningFixture(t), "org_go_fixture", "org_dining_target")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	after, err := diningCounts(db)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if plan.RowCount != 21 || plan.SessionTokensRotated != 1 {
		t.Fatalf("inspection plan = %#v", plan)
	}
	if !equalDiningCounts(before, after) {
		t.Fatalf("inspection changed destination counts: before=%v after=%v", before, after)
	}
}

func TestOfflineLegacyPortfolioDiningImportMapsOnlyDiningAndKeepsSnapshot(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dining.sqlite")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO organizations(id,name,slug) VALUES(?,?,?)", "org_dining_target", "Target", "org_dining_target"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO audit_events(id,organization_id,actor_id,actor_role,action,entity_type,entity_id) VALUES(?,?,?,?,?,?,?)", "audit_keep", "org_dining_target", "user_keep", "admin", "KEEP", "test", "audit_keep"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(dir, "portfolio-backup.json")
	if err = os.WriteFile(backupPath, portfolioDiningFixture(t), 0600); err != nil {
		t.Fatal(err)
	}
	sourceDigest := sha256.Sum256(portfolioDiningFixture(t))
	snapshotPath := filepath.Join(dir, "before-import.sqlite")
	result, err := runLegacyPortfolioDiningImport(context.Background(), dbPath, backupPath, snapshotPath, "org_go_fixture", "org_dining_target")
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 21 || result.SessionTokensRotated != 1 {
		t.Fatalf("import result = %#v", result)
	}
	if _, err = os.Stat(snapshotPath); err != nil {
		t.Fatalf("pre-import snapshot missing: %v", err)
	}
	afterSource, err := os.ReadFile(backupPath)
	if err != nil || sha256.Sum256(afterSource) != sourceDigest {
		t.Fatal("source backup changed")
	}
	db, err = openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for table, expected := range map[string]int{"dining_locations": 1, "dining_tables": 3, "dining_menu_items": 4, "dining_orders": 1, "dining_order_items": 2, "dining_kitchen_tickets": 2} {
		var count int
		if err = db.QueryRow("SELECT count(*) FROM \""+table+"\" WHERE organization_id=?", "org_dining_target").Scan(&count); err != nil || count != expected {
			t.Fatalf("%s rows = %d, err %v; want %d", table, count, err, expected)
		}
	}
	var posCount, auditCount, targetOrgCount int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='pos_sales'").Scan(&posCount); err != nil || posCount != 0 {
		t.Fatalf("non-Dining row imported: %d (%v)", posCount, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM audit_events WHERE id='audit_keep'").Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("destination audit changed: %d (%v)", auditCount, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM organizations WHERE id='org_dining_target'").Scan(&targetOrgCount); err != nil || targetOrgCount != 1 {
		t.Fatalf("destination identity changed: %d (%v)", targetOrgCount, err)
	}
	var oldToken string
	if err = db.QueryRow("SELECT session_token FROM dining_table_sessions WHERE id='din_sess_a2_org_go_fixture' AND organization_id='org_dining_target'").Scan(&oldToken); err != nil {
		t.Fatal(err)
	}
	if oldToken == "token_tbl_a2_org_go_fixture" || len(oldToken) != 64 {
		t.Fatalf("source session token was not rotated: %q", oldToken)
	}
}

func TestOfflineLegacyPortfolioDiningImportRollsBackOnConflictAndPreservesSnapshot(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "dining.sqlite")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO organizations(id,name,slug) VALUES(?,?,?)", "org_dining_target", "Target", "org_dining_target"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TRIGGER reject_import BEFORE INSERT ON dining_tables BEGIN SELECT RAISE(ABORT, 'test rollback'); END"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(dir, "backup.json")
	if err = os.WriteFile(backupPath, portfolioDiningFixture(t), 0600); err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(dir, "snapshot.sqlite")
	if _, err = runLegacyPortfolioDiningImport(context.Background(), dbPath, backupPath, snapshotPath, "org_go_fixture", "org_dining_target"); err == nil {
		t.Fatal("destination conflict accepted")
	}
	if _, err = os.Stat(snapshotPath); err != nil {
		t.Fatalf("snapshot not retained after failed import: %v", err)
	}
	db, err = openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var targetLocations, importedTables int
	if err = db.QueryRow("SELECT count(*) FROM dining_locations WHERE organization_id='org_dining_target'").Scan(&targetLocations); err != nil || targetLocations != 0 {
		t.Fatalf("conflicting target row changed: %d (%v)", targetLocations, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM dining_tables WHERE organization_id='org_dining_target'").Scan(&importedTables); err != nil || importedTables != 0 {
		t.Fatalf("partial import remained after conflict: %d (%v)", importedTables, err)
	}
}

func diningCounts(db *sql.DB) (map[string]int, error) {
	counts := make(map[string]int)
	for _, table := range legacyDiningTableNames() {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			return nil, err
		}
		counts[table] = count
	}
	return counts, nil
}

func equalDiningCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for table, count := range a {
		other, ok := b[table]
		if !ok || count != other {
			return false
		}
	}
	return true
}

func TestLegacyPortfolioDiningCommandPreview(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "dining.sqlite")
	db, err := openDB(database)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO organizations(id,name,slug) VALUES(?,?,?)", "org_dining_target", "Target", "target")
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("prepare destination: %v / %v", err, closeErr)
	}
	source := filepath.Join(dir, "portfolio.json")
	if err = os.WriteFile(source, portfolioDiningFixture(t), 0600); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(map[string]any{"database": database, "backup": source, "sourceOrganization": "org_go_fixture", "destinationOrganization": "org_dining_target"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", ".", "--legacy-dining-import")
	cmd.Stdin = bytes.NewReader(request)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("preview command failed: %v: %s", err, output)
	}
	var result legacyDiningImportResult
	if err = json.Unmarshal(output, &result); err != nil || result.RowCount != 21 || result.Snapshot != "" {
		t.Fatalf("unexpected preview: %s (%v)", output, err)
	}
}
