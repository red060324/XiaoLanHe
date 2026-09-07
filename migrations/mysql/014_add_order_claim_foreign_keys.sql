ALTER TABLE purchase_order ADD CONSTRAINT fk_purchase_order_coupon_claim FOREIGN KEY(coupon_claim_id) REFERENCES coupon_claim(id) ON DELETE RESTRICT;
