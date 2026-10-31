CREATE TABLE `audit_events` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`actor_id` text NOT NULL,
	`actor_role` text NOT NULL,
	`action` text NOT NULL,
	`entity_type` text NOT NULL,
	`entity_id` text NOT NULL,
	`before_value` text,
	`after_value` text,
	`reason` text,
	`request_id` text,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `dining_bills` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`session_id` text NOT NULL,
	`subtotal` integer NOT NULL,
	`service_charge` integer DEFAULT 0 NOT NULL,
	`discount_amount` integer DEFAULT 0 NOT NULL,
	`final_amount` integer NOT NULL,
	`paid_amount` integer DEFAULT 0 NOT NULL,
	`status` text DEFAULT 'unpaid' NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `dining_kitchen_tickets` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`order_id` text NOT NULL,
	`order_item_id` text NOT NULL,
	`station` text NOT NULL,
	`item_name` text NOT NULL,
	`quantity` integer NOT NULL,
	`modifiers_text` text,
	`status` text DEFAULT 'received' NOT NULL,
	`urgent_flag` integer DEFAULT false NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `dining_locations` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`name` text NOT NULL,
	`service_fee_rate` integer DEFAULT 10 NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `dining_menu_items` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`name` text NOT NULL,
	`category` text NOT NULL,
	`price` integer NOT NULL,
	`is_sold_out` integer DEFAULT false NOT NULL,
	`station` text DEFAULT 'hot_kitchen' NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `dining_modifier_groups` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`name` text NOT NULL,
	`min_selection` integer DEFAULT 0 NOT NULL,
	`max_selection` integer DEFAULT 1 NOT NULL,
	`is_required` integer DEFAULT false NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `dining_modifier_options` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`group_id` text NOT NULL,
	`name` text NOT NULL,
	`additional_price` integer DEFAULT 0 NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `dining_order_items` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`order_id` text NOT NULL,
	`menu_item_id` text NOT NULL,
	`item_name` text NOT NULL,
	`unit_price` integer NOT NULL,
	`quantity` integer NOT NULL,
	`modifiers_json` text,
	`line_total` integer NOT NULL,
	`status` text DEFAULT 'pending' NOT NULL,
	`cancelled_reason` text,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `dining_orders` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`session_id` text,
	`order_type` text DEFAULT 'dine_in' NOT NULL,
	`takeout_number` text,
	`subtotal` integer NOT NULL,
	`service_charge` integer DEFAULT 0 NOT NULL,
	`total_amount` integer NOT NULL,
	`status` text DEFAULT 'placed' NOT NULL,
	`idempotency_key` text,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `dining_payments` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`bill_id` text NOT NULL,
	`amount` integer NOT NULL,
	`payment_method` text DEFAULT 'cash' NOT NULL,
	`split_index` integer DEFAULT 1 NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `dining_reservations` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`location_id` text NOT NULL,
	`table_id` text NOT NULL,
	`customer_name` text NOT NULL,
	`customer_phone` text NOT NULL,
	`guest_count` integer NOT NULL,
	`reservation_time` text NOT NULL,
	`status` text DEFAULT 'confirmed' NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `dining_table_sessions` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`table_id` text NOT NULL,
	`session_token` text NOT NULL,
	`guest_count` integer DEFAULT 2 NOT NULL,
	`status` text DEFAULT 'active' NOT NULL,
	`opened_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL,
	`closed_at` text
);
--> statement-breakpoint
CREATE UNIQUE INDEX `dining_table_sessions_session_token_unique` ON `dining_table_sessions` (`session_token`);
--> statement-breakpoint
CREATE TABLE `dining_tables` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`location_id` text NOT NULL,
	`table_number` text NOT NULL,
	`capacity` integer NOT NULL,
	`status` text DEFAULT 'vacant' NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `idempotency_keys` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`key` text NOT NULL,
	`endpoint` text NOT NULL,
	`request_hash` text NOT NULL,
	`response_status` integer NOT NULL,
	`response_body` text NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `memberships` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`user_id` text NOT NULL,
	`role` text DEFAULT 'operator' NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `organizations` (
	`id` text PRIMARY KEY NOT NULL,
	`name` text NOT NULL,
	`slug` text NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE UNIQUE INDEX `organizations_slug_unique` ON `organizations` (`slug`);
--> statement-breakpoint
CREATE TABLE `outbox_events` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`event_type` text NOT NULL,
	`payload` text NOT NULL,
	`status` text DEFAULT 'pending' NOT NULL,
	`retry_count` integer DEFAULT 0 NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `users` (
	`id` text PRIMARY KEY NOT NULL,
	`email` text NOT NULL,
	`display_name` text NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);

-- Preserve existing dine-in bills while allowing a takeout bill to link directly
-- to its order without inventing a table session.
CREATE TABLE dining_bills_next (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL,
  session_id TEXT,
  order_id TEXT,
  subtotal INTEGER NOT NULL,
  service_charge INTEGER NOT NULL DEFAULT 0,
  discount_amount INTEGER NOT NULL DEFAULT 0,
  final_amount INTEGER NOT NULL,
  paid_amount INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'unpaid',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
--> statement-breakpoint
INSERT INTO dining_bills_next (id, organization_id, session_id, subtotal, service_charge, discount_amount, final_amount, paid_amount, status, created_at)
SELECT id, organization_id, session_id, subtotal, service_charge, discount_amount, final_amount, paid_amount, status, created_at FROM dining_bills;
--> statement-breakpoint
DROP TABLE dining_bills;
--> statement-breakpoint
ALTER TABLE dining_bills_next RENAME TO dining_bills;
--> statement-breakpoint
CREATE UNIQUE INDEX dining_bills_order_id_unique ON dining_bills(order_id);
--> statement-breakpoint
CREATE UNIQUE INDEX dining_orders_takeout_number_unique ON dining_orders(organization_id, takeout_number);

CREATE UNIQUE INDEX IF NOT EXISTS idempotency_scope_unique ON idempotency_keys(organization_id, key, endpoint);
--> statement-breakpoint
CREATE TABLE IF NOT EXISTS demo_seed_states (organization_id TEXT PRIMARY KEY NOT NULL, ready_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);

CREATE TABLE dining_vinext_schema(version INTEGER NOT NULL); INSERT INTO dining_vinext_schema VALUES (1);
