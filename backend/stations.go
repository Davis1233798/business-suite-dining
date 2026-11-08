package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type Dataset struct {
	Key, Table, Label string
	Front             []string
}

var datasets = map[string][]Dataset{
	"dining": {
		{"modifierGroups", "dining_modifier_groups", "Modifier groups", []string{"id", "name", "min_selection", "max_selection", "is_required"}},
		{"modifierOptions", "dining_modifier_options", "Modifier options", []string{"id", "group_id", "name", "additional_price"}},
		{"locations", "dining_locations", "Locations", []string{"id", "name", "service_fee_rate"}},
		{"sessions", "dining_table_sessions", "Table sessions", []string{"id", "table_id", "guest_count", "status", "opened_at"}},
		{"kitchenTickets", "dining_kitchen_tickets", "Kitchen tickets", []string{"id", "order_id", "order_item_id", "station", "item_name", "quantity", "modifiers_text", "status", "urgent_flag", "created_at"}},
		{"tables", "dining_tables", "Tables", []string{"id", "location_id", "table_number", "capacity", "status"}},
		{"orders", "dining_orders", "Orders", []string{"id", "session_id", "takeout_number", "status", "created_at"}},
		{"orderItems", "dining_order_items", "Order items", []string{"id", "order_id", "item_name", "quantity", "line_total", "status"}},
		{"menuItems", "dining_menu_items", "Menu", []string{"id", "name", "price", "category", "is_sold_out"}},
		{"bills", "dining_bills", "Bills", nil},
		{"payments", "dining_payments", "Payments", nil},
		{"reservations", "dining_reservations", "Reservations", []string{"id", "table_id", "guest_count", "reservation_time", "status"}},
	},
}

func camel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func queryMaps(db *sql.DB, query string, args ...any) ([]map[string]any, error) {
	rows, e := db.Query(query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	cols, e := rows.Columns()
	if e != nil {
		return nil, e
	}
	out := []map[string]any{}
	for rows.Next() {
		v := make([]any, len(cols))
		p := make([]any, len(cols))
		for i := range v {
			p[i] = &v[i]
		}
		if e = rows.Scan(p...); e != nil {
			return nil, e
		}
		m := map[string]any{}
		for i, c := range cols {
			if b, yes := v[i].([]byte); yes {
				v[i] = string(b)
			}
			m[camel(c)] = v[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (a *App) stationAPI(w http.ResponseWriter, r *http.Request) {
	site, action := r.PathValue("site"), r.PathValue("action")
	if !knownSite(site) {
		fail(w, 404, "Workspace not found")
		return
	}
	id, e := a.identity(r)
	if e != nil {
		fail(w, 401, e.Error())
		return
	}
	g := id.grant(site)
	if g == nil {
		fail(w, 403, "No access to this workspace")
		return
	}
	if action == "data" && r.Method == "GET" {
		front := r.URL.Query().Get("view") == "front"
		if !front && g.Scope != "back" {
			fail(w, 403, "Front-office accounts cannot read back-office data")
			return
		}
		requestedDataset := r.URL.Query().Get("dataset")
		search := strings.TrimSpace(r.URL.Query().Get("search"))
		if len(search) > 200 || (search != "" && requestedDataset == "") {
			fail(w, 400, "Invalid dataset search")
			return
		}
		offset := 0
		if raw := r.URL.Query().Get("offset"); raw != "" {
			var err error
			offset, err = strconv.Atoi(raw)
			if err != nil || offset < 0 || offset > 1000000 || requestedDataset == "" {
				fail(w, 400, "Invalid dataset offset")
				return
			}
		}
		out := map[string]any{}
		for _, d := range datasets[site] {
			if requestedDataset != "" && d.Key != requestedDataset {
				continue
			}
			if front && len(d.Front) == 0 {
				continue
			}
			columns := "*"
			if front {
				columns = strings.Join(d.Front, ",")
			}
			query := "SELECT " + columns + " FROM " + d.Table + " WHERE organization_id=?"
			args := []any{id.OrganizationID}
			if search != "" {
				searchColumns := d.Front
				if !front {
					searchColumns = nil
					info, err := a.db.Query("PRAGMA table_info(" + d.Table + ")")
					if err != nil {
						fail(w, 500, "Unable to search workspace data")
						return
					}
					for info.Next() {
						var cid, notnull, pk int
						var name, typ string
						var defaultValue any
						if err = info.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
							break
						}
						if name != "organization_id" {
							searchColumns = append(searchColumns, name)
						}
					}
					if err == nil {
						err = info.Err()
					}
					info.Close()
					if err != nil {
						fail(w, 500, "Unable to search workspace data")
						return
					}
				}
				terms := make([]string, 0, len(searchColumns))
				for _, column := range searchColumns {
					terms = append(terms, "instr(lower(CAST(\""+strings.ReplaceAll(column, "\"", "\"\"")+"\" AS TEXT)), lower(?)) > 0")
					args = append(args, search)
				}
				if len(terms) == 0 {
					fail(w, 400, "Dataset cannot be searched")
					return
				}
				query += " AND (" + strings.Join(terms, " OR ") + ")"
			}
			args = append(args, offset)
			rows, err := queryMaps(a.db, query+" ORDER BY rowid DESC LIMIT 500 OFFSET ?", args...)
			if err != nil {
				fail(w, 500, "Unable to read workspace data: "+d.Key)
				return
			}
			out[d.Key] = rows
		}
		if requestedDataset != "" {
			if _, exists := out[requestedDataset]; !exists {
				fail(w, 404, "Dataset unavailable")
				return
			}
			ok(w, out)
			return
		}
		if site == "dining" {
			rows, err := queryMaps(a.db, `SELECT s.id AS session_id,l.service_fee_rate,COALESCE(SUM(CASE WHEN o.status='placed' AND i.status!='cancelled' THEN i.line_total ELSE 0 END),0) AS subtotal FROM dining_table_sessions s JOIN dining_tables t ON t.id=s.table_id AND t.organization_id=s.organization_id JOIN dining_locations l ON l.id=t.location_id AND l.organization_id=s.organization_id LEFT JOIN dining_orders o ON o.session_id=s.id AND o.organization_id=s.organization_id LEFT JOIN dining_order_items i ON i.order_id=o.id AND i.organization_id=o.organization_id WHERE s.organization_id=? AND s.status='active' GROUP BY s.id,l.service_fee_rate ORDER BY s.rowid DESC LIMIT 500`, id.OrganizationID)
			if err != nil {
				fail(w, 500, "Unable to calculate table bills")
				return
			}
			out["billSummaries"] = rows
		}
		ok(w, out)
		return
	}
	if action == "users" {
		if g.Role != "admin" || g.Scope != "back" {
			fail(w, 403, "Only workspace administrators can manage accounts")
			return
		}
		if r.Method == "GET" {
			rows, e := queryMaps(a.db, `SELECT a.user_id,a.email,u.display_name,g.role,g.scope,g.enabled FROM station_accounts a JOIN users u ON u.id=a.user_id JOIN station_grants g ON g.user_id=a.user_id WHERE a.organization_id=? AND g.site=?`, id.OrganizationID, site)
			if e != nil {
				fail(w, 500, "Unable to read accounts")
				return
			}
			ok(w, rows)
			return
		}
		if r.Method == "POST" {
			a.manageUser(w, r, id, site)
			return
		}
	}
	if action == "print" && r.Method == "GET" {
		if g.Scope != "back" {
			fail(w, 403, "Print center requires back-office access")
			return
		}
		a.printData(w, r, id, site)
		return
	}
	if action == "service" && site == "dining" {
		a.diningService(w, r, id, g)
		return
	}
	if action == "order" && site == "dining" {
		a.diningOrder(w, r, id, g)
		return
	}
	if action == "table" && site == "dining" {
		a.diningTable(w, r, id, g)
		return
	}
	if action == "business" && site == "dining" {
		a.diningBusiness(w, r, id, g)
		return
	}
	fail(w, 501, "This operation has not been migrated to Go. No changes were made")
}

func (a *App) manageUser(w http.ResponseWriter, r *http.Request, id *Identity, site string) {
	var input struct {
		UserID   string `json:"userId"`
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Role     string `json:"role"`
		Scope    string `json:"scope"`
		Enabled  bool   `json:"enabled"`
	}
	if body(r, &input) != nil || (input.Role != "admin" && input.Role != "operator" && input.Role != "readonly") || (input.Scope != "front" && input.Scope != "back") || (input.Role == "admin" && input.Scope != "back") {
		fail(w, 400, "Invalid account or permission data")
		return
	}
	if input.UserID == id.UserID {
		fail(w, 409, "Ask another administrator to change your own permissions")
		return
	}
	hash := ""
	var e error
	if input.UserID == "" {
		input.Email = strings.ToLower(strings.TrimSpace(input.Email))
		if !strings.Contains(input.Email, "@") || len(input.Email) > 254 || len(input.Name) == 0 || len(input.Name) > 80 || len(input.Password) < 12 || len(input.Password) > 256 {
			fail(w, 400, "Provide a name, email and password of at least 12 characters")
			return
		}
		hash, e = passwordHash(input.Password, "")
		if e != nil {
			fail(w, 500, "Password processing failed")
			return
		}
	}
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 503, "Unable to begin account transaction")
		return
	}
	defer tx.Rollback()
	target := input.UserID
	if target == "" {
		target = token()
		if _, e = tx.Exec("INSERT INTO users(id,email,display_name) VALUES(?,?,?)", target, input.Email, input.Name); e == nil {
			_, e = tx.Exec("INSERT INTO station_accounts(user_id,organization_id,email,password_hash) VALUES(?,?,?,?)", target, id.OrganizationID, input.Email, hash)
		}
	} else {
		var n int
		e = tx.QueryRow("SELECT count(*) FROM station_accounts WHERE user_id=? AND organization_id=?", target, id.OrganizationID).Scan(&n)
		if e == nil && n != 1 {
			e = errors.New("Account does not belong to this organization")
		}
	}
	if e == nil {
		_, e = tx.Exec(`INSERT INTO station_grants(user_id,site,role,scope,enabled) VALUES(?,?,?,?,?) ON CONFLICT(user_id,site) DO UPDATE SET role=excluded.role,scope=excluded.scope,enabled=excluded.enabled`, target, site, input.Role, input.Scope, input.Enabled)
	}
	if e == nil {
		audit, _ := json.Marshal(map[string]any{"site": site, "role": input.Role, "scope": input.Scope, "enabled": input.Enabled})
		_, e = tx.Exec("INSERT INTO audit_events(id,organization_id,actor_id,actor_role,action,entity_type,entity_id,after_value) VALUES(?,?,?,'admin','STATION_GRANT','ACCOUNT',?,?)", token(), id.OrganizationID, id.UserID, target, string(audit))
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		fail(w, 409, "Account update failed. Check email uniqueness and organization membership")
		return
	}
	ok(w, map[string]string{"userId": target})
}

type PrintKind struct{ Site, Dataset, Title, Lines, ForeignKey string }

var printKinds = map[string]PrintKind{
	"dining-menu-label": {"dining", "menuItems", "Menu item label", "", ""},
	"dining-order":      {"dining", "orders", "Dining order", "dining_order_items", "order_id"},
}

func (a *App) printData(w http.ResponseWriter, r *http.Request, id *Identity, site string) {
	kind, exists := printKinds[r.URL.Query().Get("kind")]
	if !exists || kind.Site != site {
		fail(w, 400, "Unsupported print format")
		return
	}
	recordID := r.URL.Query().Get("id")
	table := ""
	for _, d := range datasets[site] {
		if d.Key == kind.Dataset {
			table = d.Table
		}
	}
	rows, e := queryMaps(a.db, "SELECT * FROM "+table+" WHERE organization_id=? AND id=?", id.OrganizationID, recordID)
	if e != nil || len(rows) != 1 {
		fail(w, 404, "Document not found in this organization")
		return
	}
	lines := []map[string]any{}
	if kind.Lines != "" {
		lines, e = queryMaps(a.db, fmt.Sprintf("SELECT * FROM %s WHERE organization_id=? AND %s=?", kind.Lines, kind.ForeignKey), id.OrganizationID, recordID)
		if e != nil {
			fail(w, 500, "Unable to read document details")
			return
		}
	}
	ok(w, map[string]any{"title": kind.Title, "record": rows[0], "lines": lines, "printedBy": id.Name, "organizationId": id.OrganizationID})
}
