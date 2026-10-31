import { eq } from "drizzle-orm";
import { getDb } from "../db/index";
import {
  organizations,
  demoSeedStates,
  users,
  memberships,
  diningLocations,
  diningTables,
  diningMenuItems,
  diningModifierGroups,
  diningModifierOptions,
  diningTableSessions,
  diningOrders,
  diningOrderItems,
  diningKitchenTickets,
} from "../db/schema";

// Fictional fixtures only: no real brands, personal phone numbers or delivery addresses.
export async function seedOrganization(orgId: string = "org_demo_default") {
  const db = getDb();

  // 1. Organization & Core Users
  const existingOrg = await db
    .select()
    .from(organizations)
    .where(eq(organizations.id, orgId))
    .limit(1);

  if (existingOrg.length === 0) {
    await db.insert(organizations).values({
      id: orgId,
      name: "示範企業 (Business Suite Enterprise)",
      slug: orgId,
    }).onConflictDoNothing();
  }

  // Users
  const adminUser = { id: `usr_${orgId}_admin`, email: "admin@suite.test", displayName: "示範管理員 (Admin)" };
  const operatorUser = { id: `usr_${orgId}_op`, email: "operator@suite.test", displayName: "示範作業員 / 現場主管" };
  const readonlyUser = { id: `usr_${orgId}_ro`, email: "guest@suite.test", displayName: "訪客 (唯讀)" };

  for (const u of [adminUser, operatorUser, readonlyUser]) {
    const exists = await db.select().from(users).where(eq(users.id, u.id)).limit(1);
    if (exists.length === 0) {
      await db.insert(users).values(u).onConflictDoNothing();
    }
  }

  // Memberships
  const memberRoles = [
    { userId: adminUser.id, role: "admin" },
    { userId: operatorUser.id, role: "operator" },
    { userId: readonlyUser.id, role: "readonly" },
  ];
  for (const m of memberRoles) {
    const memId = `mem_${orgId}_${m.userId}`;
    const exists = await db.select().from(memberships).where(eq(memberships.id, memId)).limit(1);
    if (exists.length === 0) {
      await db.insert(memberships).values({
        id: memId,
        organizationId: orgId,
        userId: m.userId,
        role: m.role,
      }).onConflictDoNothing();
    }
  }

  // ==========================================
  // 6. Dining「TableCraft」Seed
  // ==========================================
  const loc1 = { id: `din_loc_01_${orgId}`, organizationId: orgId, name: "TableCraft 示範餐廳 A", serviceFeeRate: 10 };
  await db.insert(diningLocations).values(loc1).onConflictDoNothing();

  const tblA1 = { id: `din_tbl_a1_${orgId}`, organizationId: orgId, locationId: loc1.id, tableNumber: "A1", capacity: 4, status: "vacant" };
  const tblA2 = { id: `din_tbl_a2_${orgId}`, organizationId: orgId, locationId: loc1.id, tableNumber: "A2", capacity: 4, status: "occupied" };
  const tblB1 = { id: `din_tbl_b1_${orgId}`, organizationId: orgId, locationId: loc1.id, tableNumber: "B1", capacity: 2, status: "vacant" };
  await db.insert(diningTables).values([tblA1, tblA2, tblB1]).onConflictDoNothing();

  const steak = { id: `din_menu_01_${orgId}`, organizationId: orgId, name: "炭烤特級肋眼牛排", category: "排餐", price: 450, isSoldOut: false, station: "hot_kitchen" };
  const pasta = { id: `din_menu_02_${orgId}`, organizationId: orgId, name: "松露黑醋牛肝菌燉飯", category: "主食", price: 320, isSoldOut: false, station: "hot_kitchen" };
  const milktea = { id: `din_menu_03_${orgId}`, organizationId: orgId, name: "手熬黑糖珍珠鮮奶", category: "飲品", price: 100, isSoldOut: false, station: "cold_bar" };
  const soldOutItem = { id: `din_menu_04_${orgId}`, organizationId: orgId, name: "限量法式主廚威靈頓牛排", category: "限定", price: 680, isSoldOut: true, station: "hot_kitchen" };
  await db.insert(diningMenuItems).values([steak, pasta, milktea, soldOutItem]).onConflictDoNothing();

  // Modifiers
  const modGroupSweet = { id: `din_modg_01_${orgId}`, organizationId: orgId, name: "甜度 (必選)", minSelection: 1, maxSelection: 1, isRequired: true };
  const modGroupTopping = { id: `din_modg_02_${orgId}`, organizationId: orgId, name: "加料升級", minSelection: 0, maxSelection: 3, isRequired: false };
  await db.insert(diningModifierGroups).values([modGroupSweet, modGroupTopping]).onConflictDoNothing();

  await db.insert(diningModifierOptions).values([
    { id: `din_modo_01_${orgId}`, organizationId: orgId, groupId: modGroupSweet.id, name: "無糖 (0分)", additionalPrice: 0 },
    { id: `din_modo_02_${orgId}`, organizationId: orgId, groupId: modGroupSweet.id, name: "微糖 (3分)", additionalPrice: 0 },
    { id: `din_modo_03_${orgId}`, organizationId: orgId, groupId: modGroupSweet.id, name: "正常糖 (全糖)", additionalPrice: 0 },
    { id: `din_modo_04_${orgId}`, organizationId: orgId, groupId: modGroupTopping.id, name: "雙份手工黑糖珍珠", additionalPrice: 10 },
    { id: `din_modo_05_${orgId}`, organizationId: orgId, groupId: modGroupTopping.id, name: "日式特濃奶蓋", additionalPrice: 20 },
  ]).onConflictDoNothing();

  // Active Session on Table A2 with QR token
  const sessionA2 = {
    id: `din_sess_a2_${orgId}`,
    organizationId: orgId,
    tableId: tblA2.id,
    sessionToken: `token_tbl_a2_${orgId}`,
    guestCount: 2,
    status: "active",
  };
  await db.insert(diningTableSessions).values(sessionA2).onConflictDoNothing();

  // Sample order: 100 + 200 = 300, service 30 = 330
  const orderA2 = {
    id: `din_ord_01_${orgId}`,
    organizationId: orgId,
    sessionId: sessionA2.id,
    orderType: "dine_in",
    subtotal: 300,
    serviceCharge: 30,
    totalAmount: 330,
    status: "placed",
  };
  await db.insert(diningOrders).values(orderA2).onConflictDoNothing();

  const ordItem1 = {
    id: `din_oi_01_${orgId}`,
    organizationId: orgId,
    orderId: orderA2.id,
    menuItemId: milktea.id,
    itemName: "手熬黑糖珍珠鮮奶",
    unitPrice: 100,
    quantity: 1,
    modifiersJson: JSON.stringify([{ name: "微糖 (3分)" }]),
    lineTotal: 100,
    status: "preparing",
  };
  const ordItem2 = {
    id: `din_oi_02_${orgId}`,
    organizationId: orgId,
    orderId: orderA2.id,
    menuItemId: pasta.id,
    itemName: "松露黑醋牛肝菌燉飯 (前菜分享版)",
    unitPrice: 200,
    quantity: 1,
    modifiersJson: null,
    lineTotal: 200,
    status: "preparing",
  };
  await db.insert(diningOrderItems).values([ordItem1, ordItem2]).onConflictDoNothing();

  await db.insert(diningKitchenTickets).values([
    {
      id: `din_kt_01_${orgId}`,
      organizationId: orgId,
      orderId: orderA2.id,
      orderItemId: ordItem1.id,
      station: "cold_bar",
      itemName: ordItem1.itemName,
      quantity: 1,
      modifiersText: "微糖 (3分)",
      status: "received",
      urgentFlag: false,
    },
    {
      id: `din_kt_02_${orgId}`,
      organizationId: orgId,
      orderId: orderA2.id,
      orderItemId: ordItem2.id,
      station: "hot_kitchen",
      itemName: ordItem2.itemName,
      quantity: 1,
      modifiersText: "",
      status: "received",
      urgentFlag: false,
    },
  ]).onConflictDoNothing();

  await db.insert(demoSeedStates).values({ organizationId: orgId }).onConflictDoNothing();
  return { success: true, organizationId: orgId };
}
