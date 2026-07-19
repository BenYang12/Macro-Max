-- Undo of 000005. Drop order is REVERSED from creation: basket_items
-- references baskets, so the referencing table must go first or Postgres
-- refuses the drop.
DROP TABLE basket_items;
DROP TABLE baskets;
