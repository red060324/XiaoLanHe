ALTER TABLE flash_sale_release_job ADD COLUMN claimable_at DATETIME(6) GENERATED ALWAYS AS(IF(status='pending',next_attempt_at,IF(status='leased',lease_until,NULL))) STORED;
