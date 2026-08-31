create table if not exists purchase_order (
    id bigserial primary key,
    order_no varchar(40) not null,
    user_id bigint not null references user_account(id),
    status varchar(24) not null default 'pending_payment',
    currency char(3) not null,
    region_code varchar(16) not null,
    subtotal_minor bigint not null,
    discount_minor bigint not null default 0,
    total_minor bigint not null,
    coupon_claim_id bigint references coupon_claim(id),
    idempotency_key varchar(128) not null,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint ck_purchase_order_status check (status in ('pending_payment','paid','cancelled','expired')),
    constraint ck_purchase_order_totals check (subtotal_minor >= 0 and discount_minor >= 0 and discount_minor <= subtotal_minor and total_minor = subtotal_minor - discount_minor),
    constraint ck_purchase_order_idempotency check (char_length(idempotency_key) between 8 and 128)
);
create unique index if not exists uk_purchase_order_no on purchase_order(order_no);
create unique index if not exists uk_purchase_order_idempotency on purchase_order(user_id,idempotency_key);
create unique index if not exists uk_purchase_order_coupon_claim on purchase_order(coupon_claim_id) where coupon_claim_id is not null;
create index if not exists idx_purchase_order_owner on purchase_order(user_id,created_at desc,id desc);

create table if not exists purchase_order_item (
    id bigserial primary key,
    order_id bigint not null references purchase_order(id) on delete cascade,
    edition_id bigint not null references game_edition(id),
    game_id bigint not null references game(id),
    game_slug_snapshot varchar(64) not null,
    game_name_snapshot varchar(160) not null,
    edition_code_snapshot varchar(64) not null,
    edition_name_snapshot varchar(160) not null,
    unit_price_minor bigint not null,
    quantity integer not null default 1,
    created_at timestamptz not null default now(),
    constraint ck_purchase_order_item_price check (unit_price_minor >= 0),
    constraint ck_purchase_order_item_quantity check (quantity = 1),
    constraint uk_purchase_order_item unique (order_id,edition_id)
);

create table if not exists payment_record (
    id bigserial primary key,
    order_id bigint not null references purchase_order(id),
    provider varchar(32) not null,
    provider_reference varchar(96) not null,
    status varchar(16) not null,
    amount_minor bigint not null,
    idempotency_key varchar(128) not null,
    metadata jsonb not null default '{}'::jsonb,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint ck_payment_record_status check (status in ('paid','failed')),
    constraint ck_payment_record_amount check (amount_minor >= 0),
    constraint ck_payment_record_idempotency check (char_length(idempotency_key) between 8 and 128)
);
create unique index if not exists uk_payment_record_reference on payment_record(provider,provider_reference);
create unique index if not exists uk_payment_record_idempotency on payment_record(order_id,idempotency_key);

do $$ begin
    alter table coupon_claim add constraint fk_coupon_claim_redeemed_order foreign key (redeemed_order_id) references purchase_order(id);
exception when duplicate_object then null;
end $$;
do $$ begin
    alter table game_entitlement add constraint fk_game_entitlement_source_order foreign key (source_order_id) references purchase_order(id);
exception when duplicate_object then null;
end $$;
