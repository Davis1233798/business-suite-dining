import test from "node:test";
import assert from "node:assert";
import { getDb } from "../../db/index.ts";
import { seedOrganization } from "../../lib/seed.ts";
import { DiningService } from "../../lib/services/dining-service.ts";
import { diningTableSessions, diningTables, diningOrders, diningKitchenTickets, mesWorkOrders, mesOperations, mesMaterialLots } from "../../db/schema.ts";
import { eq, and } from "drizzle-orm";

test("P3 Dining (TableCraft) Verification: D01 - D08", async () => {
  const orgId = `org_din_test_${Date.now()}`;
  await seedOrganization(orgId);

  const adminCtx = {
    organizationId: orgId,
    userId: "usr_admin",
    userEmail: "admin@test.local",
    userDisplayName: "Dining Manager",
    role: "admin" as const,
    requestId: "req_din_admin",
  };

  const staffCtx = {
    ...adminCtx,
    userId: "usr_waiter",
    role: "operator" as const,
    requestId: "req_din_staff",
  };

  const db = getDb();
  const sessionToken = `token_tbl_a2_${orgId}`;

  // D01: 售完商品強制拒絕下單
  const soldOutItemId = `din_menu_04_${orgId}`;
  await assert.rejects(
    async () => {
      await DiningService.placeOrder(staffCtx, {
        sessionToken,
        items: [{ menuItemId: soldOutItemId, quantity: 1 }],
      });
    },
    /已售完/
  );

  // D01: 必選甜度未填不得點餐 (飲品)
  const beverageId = `din_menu_03_${orgId}`; // 手熬黑糖珍珠鮮奶
  await assert.rejects(
    async () => {
      await DiningService.placeOrder(staffCtx, {
        sessionToken,
        items: [{ menuItemId: beverageId, quantity: 1 }], // no modifier selected
      });
    },
    /必須選擇「甜度/
  );

  // D01 & D04: 正確下單 (加料加價入帳 + 10%服務費)
  // 牛奶 100 + 珍珠 10 = 110
  const sweetGroup = `din_modg_01_${orgId}`;
  const toppingGroup = `din_modg_02_${orgId}`;
  const optSweet = `din_modo_02_${orgId}`; // 微糖 (+$0)
  const optTopping = `din_modo_04_${orgId}`; // 珍珠 (+$10)

  const orderRes: any = await DiningService.placeOrder(staffCtx, {
    sessionToken,
    items: [
      {
        menuItemId: beverageId,
        quantity: 1,
        modifiers: [
          { groupId: sweetGroup, optionId: optSweet },
          { groupId: toppingGroup, optionId: optTopping },
        ],
      },
    ],
    idempotencyKey: "din_idem_001",
  });

  assert.strictEqual(orderRes.subtotal, 110);
  assert.strictEqual(orderRes.serviceCharge, 11); // 10% of 110 = 11
  assert.strictEqual(orderRes.finalAmount, 121);

  // D02: 桌位預約 (人數超限提示、重複時段衝突拒絕)
  const tblB1 = `din_tbl_b1_${orgId}`; // capacity is 2
  await assert.rejects(
    async () => {
      await DiningService.reserveTable(staffCtx, {
        tableId: tblB1,
        customerName: "陳先生",
        customerPhone: "0922-000-111",
        guestCount: 5, // exceeds capacity 2
        reservationTime: "2026-10-30 19:00",
      });
    },
    /超過此桌容納上限/
  );

  const res1 = await DiningService.reserveTable(staffCtx, {
    tableId: tblB1,
    customerName: "陳先生",
    customerPhone: "0922-000-111",
    guestCount: 2,
    reservationTime: "2026-10-30 19:00",
  });
  assert.ok(res1.reservationId);

  // Duplicate time reservation -> reject (D02)
  await assert.rejects(
    async () => {
      await DiningService.reserveTable(staffCtx, {
        tableId: tblB1,
        customerName: "林小姐",
        customerPhone: "0933-111-222",
        guestCount: 2,
        reservationTime: "2026-10-30 19:00",
      });
    },
    /已有預約/
  );

  // D05: KDS 廚房出餐狀態機 (不可倒退或跳過受限狀態)
  const tickets = await db.select().from(diningKitchenTickets)
    .where(and(eq(diningKitchenTickets.organizationId, orgId)));
  const ticket = tickets[0];
  assert.strictEqual(ticket.status, "received");

  // Attempt jump to served -> reject
  await assert.rejects(
    async () => {
      await DiningService.updateKdsTicketStatus(staffCtx, {
        ticketId: ticket.id,
        newStatus: "served", // must go received -> cooking -> done -> served
      });
    },
    /非法狀態流轉/
  );

  // Normal flow: received -> cooking -> done -> served
  await DiningService.updateKdsTicketStatus(staffCtx, { ticketId: ticket.id, newStatus: "cooking" });
  await DiningService.updateKdsTicketStatus(staffCtx, { ticketId: ticket.id, newStatus: "done" });
  await DiningService.updateKdsTicketStatus(staffCtx, { ticketId: ticket.id, newStatus: "served" });

  // D07: 分單結帳 (10% 服務費，分單合計必須相等)
  const session = await db.select().from(diningTableSessions)
    .where(eq(diningTableSessions.sessionToken, sessionToken))
    .limit(1);

  // We had the seed order (300) + additional beverage order (110) = 410, 10% fee = 41 -> total 451
  // Let's split 451 into 2 payments: 251 and 200
  const splitPay = await DiningService.splitAndPayBill(staffCtx, {
    sessionId: session[0].id,
    splits: [
      { splitIndex: 1, amount: 251, paymentMethod: "cash" },
      { splitIndex: 2, amount: 200, paymentMethod: "credit_card" },
    ],
  });
  assert.strictEqual(splitPay.finalAmount, 451);
  assert.strictEqual(splitPay.paidSplits, 2);

  // D03: 結帳後舊 Token 不能追加點餐
  await assert.rejects(
    async () => {
      await DiningService.placeOrder(staffCtx, {
        sessionToken,
        items: [{ menuItemId: `din_menu_02_${orgId}`, quantity: 1 }],
      });
    },
    /原QR碼已失效/
  );
});
