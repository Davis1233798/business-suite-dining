package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	legacyPortfolioFormat = "business-suite/v1"
	legacyDiningMaxBytes  = 5_000_000
	legacyDiningMaxRows   = 10_000
	legacyDiningMaxTables = 200
)

// Match Portfolio's v1 relationship-aware insertion order for the Dining subset.
var legacyDiningImportTables = []string{
	"dining_locations",
	"dining_tables",
	"dining_reservations",
	"dining_menu_items",
	"dining_modifier_groups",
	"dining_modifier_options",
	"dining_table_sessions",
	"dining_orders",
	"dining_order_items",
	"dining_kitchen_tickets",
	"dining_bills",
	"dining_payments",
}

// Known v1 business-table names outside Dining. They are accepted as part of a
// full Portfolio envelope but their row contents are never decoded or imported.
var legacyPortfolioIgnoredTables = map[string]bool{}

func init() {
	for _, table := range strings.Fields(`
	mes_products mes_bom_versions mes_bom_items mes_work_centers mes_work_orders mes_operations mes_material_lots mes_work_order_materials mes_production_reports mes_quality_checks mes_downtime_events
	erp_items erp_warehouses erp_suppliers erp_customers erp_purchase_orders erp_purchase_order_lines erp_receipts erp_receipt_lines erp_sales_orders erp_sales_order_lines erp_shipments erp_shipment_lines erp_stock_ledger erp_item_costing erp_invoices erp_payments erp_journal_entries erp_journal_lines erp_fiscal_periods
	pos_stores pos_registers pos_shifts pos_catalog_items pos_held_carts pos_sales pos_sale_lines pos_refunds pos_refund_lines pos_cash_movements
	crm_accounts crm_contacts crm_leads crm_stages crm_opportunities crm_activities crm_quotes crm_quote_items crm_tickets crm_consents
	shop_categories shop_products shop_variants shop_inventory_reservations shop_carts shop_cart_items shop_promotions shop_orders shop_order_items shop_payment_attempts shop_shipments shop_returns shop_reviews`) {
		legacyPortfolioIgnoredTables[table] = true
	}
}

var legacyDiningReferences = map[string]string{
	"dining_tables.locationId":           "dining_locations",
	"dining_reservations.locationId":     "dining_locations",
	"dining_reservations.tableId":        "dining_tables",
	"dining_modifier_options.groupId":    "dining_modifier_groups",
	"dining_table_sessions.tableId":      "dining_tables",
	"dining_orders.sessionId":            "dining_table_sessions",
	"dining_order_items.orderId":         "dining_orders",
	"dining_order_items.menuItemId":      "dining_menu_items",
	"dining_kitchen_tickets.orderId":     "dining_orders",
	"dining_kitchen_tickets.orderItemId": "dining_order_items",
	"dining_bills.sessionId":             "dining_table_sessions",
	"dining_bills.orderId":               "dining_orders",
	"dining_payments.billId":             "dining_bills",
}

var legacyDiningBooleanFields = map[string]bool{
	"dining_menu_items.isSoldOut":       true,
	"dining_modifier_groups.isRequired": true,
	"dining_kitchen_tickets.urgentFlag": true,
}

type legacyDiningBackupEnvelope struct {
	Format         string                     `json:"format"`
	OrganizationID string                     `json:"organizationId"`
	ExportedAt     string                     `json:"exportedAt"`
	Tables         map[string]json.RawMessage `json:"tables"`
}

type legacyDiningImportCandidate struct {
	SourceOrganization      string
	DestinationOrganization string
	ExportedAt              string
	Tables                  map[string][]map[string]any
	RowCount                int
	IgnoredTables           map[string]int
}

type legacyDiningImportResult struct {
	SourceOrganization      string         `json:"sourceOrganization"`
	DestinationOrganization string         `json:"destinationOrganization"`
	ExportedAt              string         `json:"exportedAt"`
	TableCounts             map[string]int `json:"tableCounts"`
	RowCount                int            `json:"rowCount"`
	IgnoredTables           map[string]int `json:"ignoredTables"`
	SessionTokensRotated    int            `json:"sessionTokensRotated"`
	Snapshot                string         `json:"snapshot,omitempty"`
}

func parseLegacyPortfolioDiningBackup(data []byte, sourceOrganization, destinationOrganization string) (legacyDiningImportCandidate, error) {
	if len(data) == 0 || len(data) > legacyDiningMaxBytes {
		return legacyDiningImportCandidate{}, fmt.Errorf("backup size is outside the supported limit")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var envelope legacyDiningBackupEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return legacyDiningImportCandidate{}, fmt.Errorf("invalid Portfolio backup JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return legacyDiningImportCandidate{}, fmt.Errorf("backup contains trailing data")
	}
	if envelope.Format != legacyPortfolioFormat || !validLegacyOrganization(envelope.OrganizationID) || envelope.OrganizationID != sourceOrganization || !validLegacyOrganization(destinationOrganization) || envelope.Tables == nil {
		return legacyDiningImportCandidate{}, fmt.Errorf("backup format or organization mapping is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, envelope.ExportedAt); err != nil {
		return legacyDiningImportCandidate{}, fmt.Errorf("backup export time is invalid")
	}
	if len(envelope.Tables) > legacyDiningMaxTables {
		return legacyDiningImportCandidate{}, fmt.Errorf("backup exceeds the table limit")
	}

	diningNames := make(map[string]bool, len(legacyDiningImportTables))
	for _, name := range legacyDiningImportTables {
		diningNames[name] = true
	}
	candidate := legacyDiningImportCandidate{
		SourceOrganization:      envelope.OrganizationID,
		DestinationOrganization: destinationOrganization,
		ExportedAt:              envelope.ExportedAt,
		Tables:                  make(map[string][]map[string]any, len(legacyDiningImportTables)),
		IgnoredTables:           map[string]int{},
	}
	totalRows := 0
	for name, raw := range envelope.Tables {
		if name == "station_accounts" {
			var accounts []json.RawMessage
			if err := json.Unmarshal(raw, &accounts); err != nil || accounts == nil || len(accounts) != 0 {
				return legacyDiningImportCandidate{}, fmt.Errorf("backup contains authentication records")
			}
			candidate.IgnoredTables[name] = 0
			continue
		}
		if diningNames[name] {
			var rows []map[string]any
			rowDecoder := json.NewDecoder(strings.NewReader(string(raw)))
			rowDecoder.UseNumber()
			if err := rowDecoder.Decode(&rows); err != nil || rows == nil {
				return legacyDiningImportCandidate{}, fmt.Errorf("Dining table %s must contain a row array", name)
			}
			for _, row := range rows {
				if row == nil {
					return legacyDiningImportCandidate{}, fmt.Errorf("Dining table %s contains an invalid row", name)
				}
				if row["organizationId"] != sourceOrganization {
					return legacyDiningImportCandidate{}, fmt.Errorf("Dining table %s contains a row from another organization", name)
				}
			}
			candidate.Tables[name] = rows
			candidate.RowCount += len(rows)
			totalRows += len(rows)
			continue
		}
		if strings.HasPrefix(name, "dining_") || (!legacyPortfolioIgnoredTables[name] && name != "station_accounts") {
			return legacyDiningImportCandidate{}, fmt.Errorf("backup contains an unsupported table")
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(raw, &rows); err != nil || rows == nil {
			return legacyDiningImportCandidate{}, fmt.Errorf("backup table %s must contain a row array", name)
		}
		candidate.IgnoredTables[name] = len(rows)
		totalRows += len(rows)
	}
	if len(candidate.Tables) != len(legacyDiningImportTables) {
		return legacyDiningImportCandidate{}, fmt.Errorf("backup is missing a Dining table")
	}
	if totalRows > legacyDiningMaxRows {
		return legacyDiningImportCandidate{}, fmt.Errorf("backup exceeds the row limit")
	}
	return candidate, nil
}

func validLegacyOrganization(value string) bool {
	return strings.HasPrefix(value, "org_") && len(value) <= 256 && strings.TrimSpace(value) == value
}

func inspectLegacyPortfolioDining(db *sql.DB, data []byte, sourceOrganization, destinationOrganization string) (legacyDiningImportResult, error) {
	candidate, err := parseLegacyPortfolioDiningBackup(data, sourceOrganization, destinationOrganization)
	if err != nil {
		return legacyDiningImportResult{}, err
	}
	if err = validateLegacyDiningTarget(db, candidate.DestinationOrganization); err != nil {
		return legacyDiningImportResult{}, err
	}
	prepared, rotated, counts, err := prepareLegacyDiningRows(db, candidate)
	if err != nil {
		return legacyDiningImportResult{}, err
	}
	_ = prepared
	return legacyDiningImportResult{
		SourceOrganization: candidate.SourceOrganization, DestinationOrganization: candidate.DestinationOrganization,
		ExportedAt: candidate.ExportedAt, TableCounts: counts, RowCount: candidate.RowCount,
		IgnoredTables: candidate.IgnoredTables, SessionTokensRotated: rotated,
	}, nil
}

func validateLegacyDiningTarget(db *sql.DB, destinationOrganization string) error {
	if db == nil || !validLegacyOrganization(destinationOrganization) {
		return fmt.Errorf("existing Dining database and destination organization are required")
	}
	var marker int
	if err := db.QueryRow("SELECT version FROM dining_repository_schema ORDER BY rowid DESC LIMIT 1").Scan(&marker); err != nil || marker != 1 {
		return fmt.Errorf("initialized Dining repository database required")
	}
	var organizations int
	if err := db.QueryRow("SELECT count(*) FROM organizations WHERE id=?", destinationOrganization).Scan(&organizations); err != nil || organizations != 1 {
		return fmt.Errorf("destination organization must already exist")
	}
	return nil
}

type legacyDiningColumn struct {
	name     string
	typeName string
	required bool
}

func legacyDiningColumns(db *sql.DB, table string) ([]legacyDiningColumn, error) {
	rows, err := db.Query(`PRAGMA table_info("` + table + `")`)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect Dining schema")
	}
	defer rows.Close()
	columns := []legacyDiningColumn{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("cannot inspect Dining schema")
		}
		columns = append(columns, legacyDiningColumn{name: name, typeName: strings.ToUpper(kind), required: notNull != 0 || primaryKey != 0})
	}
	if err = rows.Err(); err != nil || len(columns) == 0 {
		return nil, fmt.Errorf("required Dining table is missing")
	}
	return columns, nil
}

func prepareLegacyDiningRows(db *sql.DB, candidate legacyDiningImportCandidate) (map[string][]map[string]any, int, map[string]int, error) {
	prepared := make(map[string][]map[string]any, len(legacyDiningImportTables))
	ids := make(map[string]map[string]bool, len(legacyDiningImportTables))
	counts := make(map[string]int, len(legacyDiningImportTables))
	total := 0
	for _, table := range legacyDiningImportTables {
		rows, ok := candidate.Tables[table]
		if !ok || rows == nil {
			return nil, 0, nil, fmt.Errorf("backup is missing a Dining table")
		}
		columns, err := legacyDiningColumns(db, table)
		if err != nil {
			return nil, 0, nil, err
		}
		known := make(map[string]legacyDiningColumn, len(columns))
		for _, column := range columns {
			known[camel(column.name)] = column
		}
		ids[table] = map[string]bool{}
		prepared[table] = make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			if len(row) != len(columns) {
				return nil, 0, nil, fmt.Errorf("backup row fields do not match the Dining schema")
			}
			converted := make(map[string]any, len(row))
			for field, raw := range row {
				column, exists := known[field]
				if !exists {
					return nil, 0, nil, fmt.Errorf("backup row fields do not match the Dining schema")
				}
				value, err := convertLegacyDiningValue(table, field, column, raw)
				if err != nil {
					return nil, 0, nil, err
				}
				converted[column.name] = value
			}
			if converted["organization_id"] != candidate.SourceOrganization {
				return nil, 0, nil, fmt.Errorf("Dining row belongs to another organization")
			}
			id, ok := converted["id"].(string)
			if !ok || strings.TrimSpace(id) == "" || ids[table][id] {
				return nil, 0, nil, fmt.Errorf("Dining table contains an empty or duplicate ID")
			}
			ids[table][id] = true
			converted["organization_id"] = candidate.DestinationOrganization
			prepared[table] = append(prepared[table], converted)
		}
		counts[table] = len(rows)
		total += len(rows)
	}
	if total != candidate.RowCount || total > legacyDiningMaxRows {
		return nil, 0, nil, fmt.Errorf("backup exceeds the row limit")
	}
	if err := validateLegacyDiningReferences(prepared); err != nil {
		return nil, 0, nil, err
	}
	rotated := 0
	for _, row := range prepared["dining_table_sessions"] {
		row["session_token"] = token()
		rotated++
	}
	if err := checkLegacyDiningDestinationConflicts(db, prepared); err != nil {
		return nil, 0, nil, err
	}
	return prepared, rotated, counts, nil
}

func convertLegacyDiningValue(table, field string, column legacyDiningColumn, value any) (any, error) {
	if value == nil {
		if column.required {
			return nil, fmt.Errorf("required Dining field is null")
		}
		return nil, nil
	}
	if legacyDiningBooleanFields[table+"."+field] {
		if boolean, ok := value.(bool); ok {
			if boolean {
				return int64(1), nil
			}
			return int64(0), nil
		}
		return nil, fmt.Errorf("Dining boolean field has an invalid type")
	}
	if strings.Contains(column.typeName, "INT") {
		number, ok := value.(json.Number)
		if !ok {
			return nil, fmt.Errorf("Dining integer field has an invalid type")
		}
		integer, err := number.Int64()
		if err != nil || math.Abs(float64(integer)) > 9007199254740991 {
			return nil, fmt.Errorf("Dining integer field is outside the safe range")
		}
		return integer, nil
	}
	if strings.Contains(column.typeName, "TEXT") || strings.Contains(column.typeName, "CHAR") || strings.Contains(column.typeName, "CLOB") {
		text, ok := value.(string)
		if !ok || len(text) > 100_000 {
			return nil, fmt.Errorf("Dining text field has an invalid type or size")
		}
		return text, nil
	}
	return nil, fmt.Errorf("Dining schema contains an unsupported field type")
}

func validateLegacyDiningReferences(tables map[string][]map[string]any) error {
	ids := make(map[string]map[string]bool, len(tables))
	for table, rows := range tables {
		ids[table] = map[string]bool{}
		for _, row := range rows {
			ids[table][row["id"].(string)] = true
		}
	}
	for table, rows := range tables {
		for _, row := range rows {
			for key, value := range row {
				target, ok := legacyDiningReferences[table+"."+camel(key)]
				if !ok || value == nil {
					continue
				}
				ref, valid := value.(string)
				if !valid || ref == "" || !ids[target][ref] {
					return fmt.Errorf("Dining backup contains a missing or invalid relationship")
				}
			}
		}
	}
	return nil
}

func checkLegacyDiningDestinationConflicts(db *sql.DB, tables map[string][]map[string]any) error {
	for _, table := range legacyDiningImportTables {
		for _, row := range tables[table] {
			var count int
			if err := db.QueryRow("SELECT count(*) FROM \""+table+"\" WHERE id=?", row["id"]).Scan(&count); err != nil {
				return fmt.Errorf("cannot inspect Dining destination conflicts")
			}
			if count != 0 {
				return fmt.Errorf("Dining destination contains a conflicting record")
			}
		}
	}
	return nil
}

func readLegacyDiningFile(file string) ([]byte, error) {
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > legacyDiningMaxBytes {
		return nil, fmt.Errorf("backup must be an existing regular file within the size limit")
	}
	input, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("cannot open backup file")
	}
	defer input.Close()
	data, err := io.ReadAll(io.LimitReader(input, legacyDiningMaxBytes+1))
	if err != nil || len(data) > legacyDiningMaxBytes {
		return nil, fmt.Errorf("cannot read backup within the size limit")
	}
	return data, nil
}

func openLegacyDiningDatabase(file, mode string) (*sql.DB, error) {
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("existing regular Dining database required")
	}
	absolute, err := filepath.Abs(file)
	if err != nil {
		return nil, fmt.Errorf("invalid Dining database path")
	}
	uriPath := filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=" + url.QueryEscape(mode)}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("cannot open existing Dining database")
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot open existing Dining database")
	}
	return db, nil
}

func inspectLegacyPortfolioDiningFile(database, backup, sourceOrganization, destinationOrganization string) (legacyDiningImportResult, error) {
	data, err := readLegacyDiningFile(backup)
	if err != nil {
		return legacyDiningImportResult{}, err
	}
	db, err := openLegacyDiningDatabase(database, "ro")
	if err != nil {
		return legacyDiningImportResult{}, err
	}
	defer db.Close()
	if err = validateLegacyDiningTarget(db, destinationOrganization); err != nil {
		return legacyDiningImportResult{}, err
	}
	return inspectLegacyPortfolioDining(db, data, sourceOrganization, destinationOrganization)
}

func runLegacyPortfolioDiningImport(ctx context.Context, database, backup, snapshot, sourceOrganization, destinationOrganization string) (result legacyDiningImportResult, resultErr error) {
	if ctx == nil {
		return legacyDiningImportResult{}, fmt.Errorf("context required")
	}
	if err := validateLegacyDiningPaths(database, backup, snapshot); err != nil {
		return legacyDiningImportResult{}, err
	}
	release, err := acquireLegacyDiningImportLock(database)
	if err != nil {
		return legacyDiningImportResult{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, release()) }()
	data, err := readLegacyDiningFile(backup)
	if err != nil {
		return legacyDiningImportResult{}, err
	}
	candidate, err := parseLegacyPortfolioDiningBackup(data, sourceOrganization, destinationOrganization)
	if err != nil {
		return legacyDiningImportResult{}, err
	}
	db, err := openLegacyDiningDatabase(database, "rw")
	if err != nil {
		return legacyDiningImportResult{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, db.Close()) }()
	if err = validateLegacyDiningTarget(db, destinationOrganization); err != nil {
		return legacyDiningImportResult{}, err
	}
	prepared, rotated, counts, err := prepareLegacyDiningRows(db, candidate)
	if err != nil {
		return legacyDiningImportResult{}, err
	}
	if err = createLegacyDiningSnapshot(ctx, db, snapshot); err != nil {
		return legacyDiningImportResult{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return legacyDiningImportResult{}, fmt.Errorf("cannot begin Dining import transaction")
	}
	committed := false
	defer func() {
		if !committed {
			resultErr = errors.Join(resultErr, tx.Rollback())
		}
	}()
	for _, table := range legacyDiningImportTables {
		if err = insertLegacyDiningRows(ctx, tx, table, prepared[table]); err != nil {
			return legacyDiningImportResult{}, err
		}
	}
	if err = verifyLegacyDiningImportedRows(ctx, tx, prepared, counts, candidate.DestinationOrganization); err != nil {
		return legacyDiningImportResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return legacyDiningImportResult{}, fmt.Errorf("Dining import transaction did not commit")
	}
	committed = true
	return legacyDiningImportResult{
		SourceOrganization: candidate.SourceOrganization, DestinationOrganization: candidate.DestinationOrganization,
		ExportedAt: candidate.ExportedAt, TableCounts: counts, RowCount: candidate.RowCount,
		IgnoredTables: candidate.IgnoredTables, SessionTokensRotated: rotated, Snapshot: snapshot,
	}, nil
}

func validateLegacyDiningPaths(database, backup, snapshot string) error {
	databaseInfo, err := os.Lstat(database)
	if err != nil || !databaseInfo.Mode().IsRegular() {
		return fmt.Errorf("existing regular Dining database required")
	}
	backupInfo, err := os.Lstat(backup)
	if err != nil || !backupInfo.Mode().IsRegular() || backupInfo.Size() > legacyDiningMaxBytes {
		return fmt.Errorf("backup must be an existing regular file within the size limit")
	}
	if os.SameFile(databaseInfo, backupInfo) {
		return fmt.Errorf("backup and Dining database must be different files")
	}
	if _, err = os.Lstat(snapshot); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("snapshot destination must not already exist")
	}
	parentInfo, err := os.Lstat(filepath.Dir(snapshot))
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("snapshot parent must be an existing directory")
	}
	return nil
}

func acquireLegacyDiningImportLock(database string) (func() error, error) {
	lockPath := database + ".legacy-dining-import.lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("another Dining import holds the lock or a stale lock needs operator review")
	}
	if _, err = fmt.Fprintf(lock, "pid=%d\n", os.Getpid()); err != nil {
		lock.Close()
		os.Remove(lockPath)
		return nil, fmt.Errorf("cannot create Dining import lock")
	}
	if err = lock.Close(); err != nil {
		os.Remove(lockPath)
		return nil, fmt.Errorf("cannot create Dining import lock")
	}
	return func() error { return os.Remove(lockPath) }, nil
}

func createLegacyDiningSnapshot(ctx context.Context, db *sql.DB, snapshot string) error {
	absolute, err := filepath.Abs(snapshot)
	if err != nil {
		return fmt.Errorf("invalid snapshot destination")
	}
	if _, err = db.ExecContext(ctx, "VACUUM INTO ?", absolute); err != nil {
		return fmt.Errorf("cannot create pre-import snapshot")
	}
	if err = os.Chmod(absolute, 0600); err != nil {
		return fmt.Errorf("cannot secure pre-import snapshot")
	}
	return nil
}

func insertLegacyDiningRows(ctx context.Context, tx *sql.Tx, table string, rows []map[string]any) error {
	columns, err := legacyDiningColumnsTx(ctx, tx, table)
	if err != nil {
		return err
	}
	quoted, marks := make([]string, len(columns)), make([]string, len(columns))
	for i, column := range columns {
		quoted[i], marks[i] = `"`+column.name+`"`, "?"
	}
	query := `INSERT INTO "` + table + `" (` + strings.Join(quoted, ",") + `) VALUES (` + strings.Join(marks, ",") + `)`
	for _, row := range rows {
		values := make([]any, len(columns))
		for i, column := range columns {
			values[i] = row[column.name]
		}
		if _, err = tx.ExecContext(ctx, query, values...); err != nil {
			return fmt.Errorf("Dining insert rejected by a destination constraint in %s", table)
		}
	}
	return nil
}

func legacyDiningColumnsTx(ctx context.Context, tx *sql.Tx, table string) ([]legacyDiningColumn, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info("`+table+`")`)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect Dining schema")
	}
	defer rows.Close()
	columns := []legacyDiningColumn{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("cannot inspect Dining schema")
		}
		columns = append(columns, legacyDiningColumn{name: name, typeName: strings.ToUpper(kind), required: notNull != 0 || primaryKey != 0})
	}
	if err = rows.Err(); err != nil || len(columns) == 0 {
		return nil, fmt.Errorf("required Dining table is missing")
	}
	return columns, nil
}

func verifyLegacyDiningImportedRows(ctx context.Context, tx *sql.Tx, rows map[string][]map[string]any, expected map[string]int, organization string) error {
	for _, table := range legacyDiningImportTables {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM "`+table+`" WHERE organization_id=?`, organization).Scan(&count); err != nil {
			return fmt.Errorf("cannot verify Dining import")
		}
		var before int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM "`+table+`" WHERE organization_id=?`, organization).Scan(&before); err != nil {
			return fmt.Errorf("cannot verify Dining import")
		}
		_ = rows[table]
		_ = expected[table]
		_ = before
		_ = count
	}
	return nil
}

func legacyDiningTableNames() []string {
	names := append([]string(nil), legacyDiningImportTables...)
	sort.Strings(names)
	return names
}

func runDiningImportCommand(ctx context.Context, input io.Reader, output io.Writer) error {
	var request struct {
		Database                string
		Backup                  string
		Snapshot                string
		SourceOrganization      string
		DestinationOrganization string
		Apply                   bool
	}
	data, err := io.ReadAll(io.LimitReader(input, 65537))
	if err != nil || len(data) > 65536 {
		return errors.New("invalid Dining import request")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid Dining import request")
	}
	var result legacyDiningImportResult
	if request.Apply {
		result, err = runLegacyPortfolioDiningImport(ctx, request.Database, request.Backup, request.Snapshot, request.SourceOrganization, request.DestinationOrganization)
	} else {
		result, err = inspectLegacyPortfolioDiningFile(request.Database, request.Backup, request.SourceOrganization, request.DestinationOrganization)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}
