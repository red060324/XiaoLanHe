ALTER TABLE coupon_claim ADD CONSTRAINT fk_coupon_claim_redeemed_order FOREIGN KEY(redeemed_order_id) REFERENCES purchase_order(id) ON DELETE RESTRICT;
