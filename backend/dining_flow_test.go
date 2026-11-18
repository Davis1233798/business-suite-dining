package main

import (
	"encoding/json"
	"testing"
)

func TestDiningTableCapacityAndReservationConflictFlow(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	staff := loginSite(t, a, "front@suite.test", space, "dining")
	other := loginSite(t, a, "admin@suite.test", token(), "dining")

	// Locate a table with capacity 2
	var table string
	var capacity int
	if e := a.db.QueryRow("SELECT id,capacity FROM dining_tables WHERE organization_id=? AND capacity=2 LIMIT 1", org).Scan(&table, &capacity); e != nil {
		t.Fatal(e)
	}

	// 1. Capacity Exceeded Rejection: 5 guests for a 2-person table
	resPath := "/api/stations/dining/service"
	overCapacity := map[string]any{
		"action":          "reserve",
		"tableId":         table,
		"name":            "Over Capacity Guest",
		"phone":           "0911000111",
		"guests":          capacity + 3,
		"reservationTime": "2036-05-10T18:00:00Z",
		"idempotencyKey":  "reserve-overcap-0001",
	}
	if w := request(a, "POST", resPath, overCapacity, staff...); w.Code != 409 {
		t.Fatalf("expected 409 for over-capacity reservation, got %d: %s", w.Code, w.Body.String())
	}

	// Also verify open table rejects guest count exceeding capacity
	tblPath := "/api/stations/dining/table"
	openOverCapacity := map[string]any{
		"action":         "open",
		"tableId":        table,
		"guests":         capacity + 2,
		"idempotencyKey": "open-overcap-0001",
	}
	if w := request(a, "POST", tblPath, openOverCapacity, staff...); w.Code != 409 {
		t.Fatalf("expected 409 for over-capacity table opening, got %d: %s", w.Code, w.Body.String())
	}

	// 2. Successful reservation within capacity
	validReservation := map[string]any{
		"action":          "reserve",
		"tableId":         table,
		"name":            "Valid Guest",
		"phone":           "0911000222",
		"guests":          capacity,
		"reservationTime": "2036-05-10T18:00:00Z",
		"idempotencyKey":  "reserve-valid-0001",
	}
	if w := request(a, "POST", resPath, validReservation, staff...); w.Code != 200 {
		t.Fatalf("valid reservation failed: %d %s", w.Code, w.Body.String())
	}

	// 3. Overlapping reservation rejection (409): same table within 90-minute window
	overlapping := map[string]any{
		"action":          "reserve",
		"tableId":         table,
		"name":            "Conflicting Guest",
		"phone":           "0911000333",
		"guests":          capacity,
		"reservationTime": "2036-05-10T18:45:00Z", // 45 minutes later, overlaps 90m slot
		"idempotencyKey":  "reserve-conflict-0001",
	}
	if w := request(a, "POST", resPath, overlapping, staff...); w.Code != 409 {
		t.Fatalf("expected 409 for overlapping reservation, got %d: %s", w.Code, w.Body.String())
	}

	// Non-overlapping reservation (>=90 minutes later) succeeds
	nonOverlapping := map[string]any{
		"action":          "reserve",
		"tableId":         table,
		"name":            "Later Guest",
		"phone":           "0911000444",
		"guests":          capacity,
		"reservationTime": "2036-05-10T19:30:00Z", // exactly 90 minutes later
		"idempotencyKey":  "reserve-nonconflict-0001",
	}
	if w := request(a, "POST", resPath, nonOverlapping, staff...); w.Code != 200 {
		t.Fatalf("expected 200 for non-overlapping reservation, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Same table cannot have two active unsettled orders/sessions simultaneously
	openValid := func(key string) map[string]any {
		return map[string]any{
			"action":         "open",
			"tableId":        table,
			"guests":         capacity,
			"idempotencyKey": key,
		}
	}
	// First open succeeds
	if w := request(a, "POST", tblPath, openValid("open-sess-0001"), staff...); w.Code != 200 {
		t.Fatalf("initial table open failed: %d %s", w.Code, w.Body.String())
	}
	// Second open on the same table while still active/unsettled must be rejected with 409
	if w := request(a, "POST", tblPath, openValid("open-sess-0002"), staff...); w.Code != 409 {
		t.Fatalf("expected 409 for concurrent/duplicate table open, got %d: %s", w.Code, w.Body.String())
	}

	// Foreign tenant cannot open or reserve this table (404)
	if w := request(a, "POST", resPath, validReservation, other...); w.Code != 404 {
		t.Fatalf("foreign reservation attempt: expected 404, got %d", w.Code)
	}
}

func TestDiningKdsStateMachineStrictTransitions(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	cook := loginSite(t, a, "front@suite.test", space, "dining")

	var ticket, item string
	if e := a.db.QueryRow("SELECT id,order_item_id FROM dining_kitchen_tickets WHERE organization_id=? AND status='received' LIMIT 1", org).Scan(&ticket, &item); e != nil {
		t.Fatal(e)
	}
	path := "/api/stations/dining/business"

	// 1. Illegal forward skip: from received jump directly to served (reject 409 or 400)
	skipReq := map[string]any{"ticketId": ticket, "expected": "received", "next": "served"}
	if w := request(a, "POST", path, skipReq, cook...); w.Code != 400 && w.Code != 409 {
		t.Fatalf("expected 400 or 409 for forward skip, got %d: %s", w.Code, w.Body.String())
	}

	// 2. Normal sequential transitions: received -> cooking -> done -> served
	seq := [][2]string{
		{"received", "cooking"},
		{"cooking", "done"},
		{"done", "served"},
	}
	for _, s := range seq {
		w := request(a, "POST", path, map[string]any{"ticketId": ticket, "expected": s[0], "next": s[1]}, cook...)
		if w.Code != 200 {
			t.Fatalf("expected 200 for valid transition %s->%s, got %d: %s", s[0], s[1], w.Code, w.Body.String())
		}
	}

	// Verify terminal state
	var tStatus, iStatus string
	a.db.QueryRow("SELECT status FROM dining_kitchen_tickets WHERE id=? AND organization_id=?", ticket, org).Scan(&tStatus)
	a.db.QueryRow("SELECT status FROM dining_order_items WHERE id=? AND organization_id=?", item, org).Scan(&iStatus)
	if tStatus != "served" || iStatus != "served" {
		t.Fatalf("expected served state, got ticket=%s item=%s", tStatus, iStatus)
	}

	// 3. Illegal reverse transition: attempting to reverse from served back to cooking/preparing must be rejected with 409
	revReq := map[string]any{"ticketId": ticket, "expected": "served", "next": "cooking"}
	if w := request(a, "POST", path, revReq, cook...); w.Code != 409 {
		t.Fatalf("expected 409 for illegal reverse transition from served, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Stale state mismatch rejection: expected "done" but current DB state is "served"
	staleReq := map[string]any{"ticketId": ticket, "expected": "done", "next": "served"}
	if w := request(a, "POST", path, staleReq, cook...); w.Code != 409 {
		t.Fatalf("expected 409 for stale state mismatch, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDiningSplitSettlementServiceFeeConservation(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	staff := loginSite(t, a, "front@suite.test", space, "dining")

	// 1. Find vacant table, open session
	var table string
	if e := a.db.QueryRow("SELECT id FROM dining_tables WHERE organization_id=? AND status='vacant' LIMIT 1", org).Scan(&table); e != nil {
		t.Fatal(e)
	}
	openRes := request(a, "POST", "/api/stations/dining/table", map[string]any{
		"action":         "open",
		"tableId":        table,
		"guests":         2,
		"idempotencyKey": "split-open-0001",
	}, staff...)
	if openRes.Code != 200 {
		t.Fatalf("open table failed: %d %s", openRes.Code, openRes.Body.String())
	}
	var openData struct {
		Data struct {
			SessionID string `json:"sessionId"`
		} `json:"data"`
	}
	json.Unmarshal(openRes.Body.Bytes(), &openData)
	session := openData.Data.SessionID

	// 2. Add two menu items: item 1 (100) + item 2 (200) = 300 subtotal
	// Insert explicit menu items to test exact D07 formula: 100 + 200 = 300, 10% fee = 30, total = 330
	item1 := "menu_d07_1_" + token()
	item2 := "menu_d07_2_" + token()
	a.db.Exec("INSERT INTO dining_menu_items(id,organization_id,name,category,price,is_sold_out,station) VALUES(?,?,'Dish A','Hot',100,0,'hot_kitchen')", item1, org)
	a.db.Exec("INSERT INTO dining_menu_items(id,organization_id,name,category,price,is_sold_out,station) VALUES(?,?,'Dish B','Hot',200,0,'hot_kitchen')", item2, org)

	orderReq := map[string]any{
		"orderType": "dine_in",
		"sessionId": session,
		"items": []any{
			map[string]any{"menuItemId": item1, "quantity": 1},
			map[string]any{"menuItemId": item2, "quantity": 1},
		},
		"idempotencyKey": "split-order-0001",
	}
	orderRes := request(a, "POST", "/api/stations/dining/order", orderReq, staff...)
	if orderRes.Code != 200 {
		t.Fatalf("order failed: %d %s", orderRes.Code, orderRes.Body.String())
	}
	var oData struct {
		Data struct {
			Subtotal      int64 `json:"subtotal"`
			ServiceCharge int64 `json:"serviceCharge"`
			Total         int64 `json:"total"`
		} `json:"data"`
	}
	json.Unmarshal(orderRes.Body.Bytes(), &oData)
	if oData.Data.Subtotal != 300 || oData.Data.ServiceCharge != 30 || oData.Data.Total != 330 {
		t.Fatalf("D07 calculation mismatch: expected subtotal=300, fee=30, total=330; got %d, %d, %d", oData.Data.Subtotal, oData.Data.ServiceCharge, oData.Data.Total)
	}

	// 3. Test Custom Splits Amount Conservation (D07)
	// Try invalid split: 200 + 120 = 320 != 330 -> must be rejected
	mismatchSplit := map[string]any{
		"action":        "pay",
		"sessionId":     session,
		"expectedTotal": 330,
		"splits": []any{
			map[string]any{"splitIndex": 1, "amount": 200, "paymentMethod": "cash"},
			map[string]any{"splitIndex": 2, "amount": 120, "paymentMethod": "mock_card"},
		},
		"idempotencyKey": "split-mismatch-0001",
	}
	if w := request(a, "POST", "/api/stations/dining/table", mismatchSplit, staff...); w.Code != 400 && w.Code != 409 {
		t.Fatalf("expected rejection for split sum mismatch, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Test Valid Custom Splits Sum == 330 (e.g. 200 + 130 = 330)
	validSplit := map[string]any{
		"action":        "pay",
		"sessionId":     session,
		"expectedTotal": 330,
		"splits": []any{
			map[string]any{"splitIndex": 1, "amount": 200, "paymentMethod": "cash"},
			map[string]any{"splitIndex": 2, "amount": 130, "paymentMethod": "mock_card"},
		},
		"idempotencyKey": "split-valid-0001",
	}
	payRes := request(a, "POST", "/api/stations/dining/table", validSplit, staff...)
	if payRes.Code != 200 {
		t.Fatalf("split pay failed: %d %s", payRes.Code, payRes.Body.String())
	}
	var pData struct {
		Data struct {
			BillID string `json:"billId"`
			Total  int64  `json:"total"`
		} `json:"data"`
	}
	json.Unmarshal(payRes.Body.Bytes(), &pData)

	// Verify payment records sum equals total exactly
	var paymentSum int64
	var paymentCount int
	a.db.QueryRow("SELECT COALESCE(SUM(amount),0), COUNT(*) FROM dining_payments WHERE organization_id=? AND bill_id=?", org, pData.Data.BillID).Scan(&paymentSum, &paymentCount)
	if paymentSum != 330 || paymentCount != 2 {
		t.Fatalf("payment sum conservation error: expected 330 across 2 payments, got sum=%d count=%d", paymentSum, paymentCount)
	}

	// Verify session closed and table released to vacant
	var sessStatus, tblStatus string
	a.db.QueryRow("SELECT status FROM dining_table_sessions WHERE id=? AND organization_id=?", session, org).Scan(&sessStatus)
	a.db.QueryRow("SELECT status FROM dining_tables WHERE id=? AND organization_id=?", table, org).Scan(&tblStatus)
	if sessStatus != "closed" || tblStatus != "vacant" {
		t.Fatalf("session or table state not updated: session=%s table=%s", sessStatus, tblStatus)
	}
}

func TestDiningRefundAuditAndReverseLink(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "dining")
	staff := loginSite(t, a, "front@suite.test", space, "dining")

	// Create a takeout order that is prepaid
	var menu string
	a.db.QueryRow("SELECT id FROM dining_menu_items WHERE organization_id=? AND is_sold_out=0 AND category NOT IN ('飲品','Drinks') LIMIT 1", org).Scan(&menu)
	orderReq := map[string]any{
		"orderType":      "takeout",
		"paymentMethod":  "mock_card",
		"items":          []any{map[string]any{"menuItemId": menu, "quantity": 1}},
		"idempotencyKey": "refund-flow-order-0001",
	}
	w := request(a, "POST", "/api/stations/dining/order", orderReq, staff...)
	if w.Code != 200 {
		t.Fatalf("order failed: %d %s", w.Code, w.Body.String())
	}
	var v struct {
		Data struct {
			OrderID string `json:"orderId"`
			Total   int64  `json:"total"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &v)
	orderID := v.Data.OrderID
	total := v.Data.Total

	// 1. Staff (non-admin) cannot refund -> 403
	refundReq := func(key string) map[string]any {
		return map[string]any{
			"action":         "refund",
			"orderId":        orderID,
			"reason":         "Customer cancellation",
			"idempotencyKey": key,
		}
	}
	if w := request(a, "POST", "/api/stations/dining/service", refundReq("refund-staff-0001"), staff...); w.Code != 403 {
		t.Fatalf("expected 403 for staff refund, got %d", w.Code)
	}

	// 2. Admin refund succeeds
	refRes := request(a, "POST", "/api/stations/dining/service", refundReq("refund-admin-0001"), admin...)
	if refRes.Code != 200 {
		t.Fatalf("admin refund failed: %d %s", refRes.Code, refRes.Body.String())
	}

	// 3. Duplicate refund rejection: attempting refund again must be rejected with 409
	dupRes := request(a, "POST", "/api/stations/dining/service", refundReq("refund-admin-0002"), admin...)
	if dupRes.Code != 409 {
		t.Fatalf("expected 409 for duplicate refund, got %d: %s", dupRes.Code, dupRes.Body.String())
	}

	// 4. Verify reverse link to original bill and audit trail
	var billID, billStatus string
	var paidAmount int64
	a.db.QueryRow("SELECT id, status, paid_amount FROM dining_bills WHERE organization_id=? AND order_id=?", org, orderID).Scan(&billID, &billStatus, &paidAmount)
	if billStatus != "refunded" || paidAmount != 0 {
		t.Fatalf("expected bill status refunded and paidAmount 0, got %s, %d", billStatus, paidAmount)
	}

	// Negative payment entry with matching bill_id
	var negAmount int64
	var payMethod string
	a.db.QueryRow("SELECT amount, payment_method FROM dining_payments WHERE organization_id=? AND bill_id=? AND amount < 0", org, billID).Scan(&negAmount, &payMethod)
	if negAmount != -total || payMethod != "mock_refund" {
		t.Fatalf("expected negative payment %d mock_refund, got %d %s", -total, negAmount, payMethod)
	}

	// Audit trail recorded
	var auditCount int
	a.db.QueryRow("SELECT count(*) FROM audit_events WHERE organization_id=? AND entity_id=? AND action LIKE 'dining.%'", org, orderID).Scan(&auditCount)
	if auditCount < 1 {
		t.Fatalf("audit event not found for refunded order %s", orderID)
	}
}

func TestDiningCrossTenantIsolationAndIdempotentRetry(t *testing.T) {
	a := appForTest(t)
	space1 := token()
	space2 := token()
	org1 := "org_test_" + space1
	user1 := loginSite(t, a, "admin@suite.test", space1, "dining")
	user2 := loginSite(t, a, "admin@suite.test", space2, "dining")

	// Get a table from org1
	var table1 string
	a.db.QueryRow("SELECT id FROM dining_tables WHERE organization_id=? LIMIT 1", org1).Scan(&table1)

	// User2 attempts to reserve org1's table -> 404
	resReq := map[string]any{
		"action":          "reserve",
		"tableId":         table1,
		"name":            "Cross Tenant Hacker",
		"phone":           "0900999888",
		"guests":          2,
		"reservationTime": "2036-07-01T12:00:00Z",
		"idempotencyKey":  "cross-res-0001",
	}
	if w := request(a, "POST", "/api/stations/dining/service", resReq, user2...); w.Code != 404 {
		t.Fatalf("expected 404 for cross-tenant reservation, got %d", w.Code)
	}

	// User1 executes reservation with idempotency key
	if w := request(a, "POST", "/api/stations/dining/service", resReq, user1...); w.Code != 200 {
		t.Fatalf("expected 200 for user1 reservation, got %d", w.Code)
	}

	// Idempotent retry with identical payload returns 200 with identical response
	retry1 := request(a, "POST", "/api/stations/dining/service", resReq, user1...)
	if retry1.Code != 200 {
		t.Fatalf("expected 200 for idempotent retry, got %d", retry1.Code)
	}

	// Idempotency key reuse with different payload returns 409
	tampered := map[string]any{
		"action":          "reserve",
		"tableId":         table1,
		"name":            "Tampered Name",
		"phone":           "0900999888",
		"guests":          2,
		"reservationTime": "2036-07-01T12:00:00Z",
		"idempotencyKey":  "cross-res-0001",
	}
	if w := request(a, "POST", "/api/stations/dining/service", tampered, user1...); w.Code != 409 {
		t.Fatalf("expected 409 for idempotency key reuse with different body, got %d", w.Code)
	}
}
