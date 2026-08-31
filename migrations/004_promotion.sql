create table if not exists coupon_campaign (
    id bigserial primary key,
    code varchar(32) not null,
    name varchar(160) not null,
    status varchar(16) not null default 'draft',
    starts_at timestamptz not null,
    ends_at timestamptz not null,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint ck_coupon_campaign_status check (status in ('draft', 'active', 'paused', 'ended')),
    constraint ck_coupon_campaign_window check (ends_at > starts_at)
);
create unique index if not exists uk_coupon_campaign_code on coupon_campaign(lower(code));
create index if not exists idx_coupon_campaign_active on coupon_campaign(status, starts_at, ends_at);

create table if not exists coupon_definition (
    id bigserial primary key,
    campaign_id bigint not null references coupon_campaign(id),
    code varchar(32) not null,
    name varchar(160) not null,
    discount_type varchar(16) not null,
    fixed_minor bigint,
    percentage_bps integer,
    currency char(3) not null,
    minimum_minor bigint not null default 0,
    total_stock bigint not null,
    claimed_stock bigint not null default 0,
    per_user_limit integer not null default 1,
    game_id bigint references game(id),
    edition_id bigint references game_edition(id),
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint ck_coupon_definition_discount check (
        (discount_type = 'fixed' and fixed_minor > 0 and percentage_bps is null) or
        (discount_type = 'percentage' and fixed_minor is null and percentage_bps between 1 and 10000)
    ),
    constraint ck_coupon_definition_minimum check (minimum_minor >= 0),
    constraint ck_coupon_definition_stock check (total_stock > 0 and claimed_stock between 0 and total_stock),
    constraint ck_coupon_definition_limit check (per_user_limit > 0),
    constraint ck_coupon_definition_scope check (edition_id is null or game_id is not null)
);
create unique index if not exists uk_coupon_definition_code on coupon_definition(lower(code));
create index if not exists idx_coupon_definition_campaign on coupon_definition(campaign_id, id desc);
create index if not exists idx_coupon_definition_game on coupon_definition(game_id, id desc);

create table if not exists coupon_claim (
    id bigserial primary key,
    coupon_id bigint not null references coupon_definition(id),
    user_id bigint not null references user_account(id),
    status varchar(16) not null default 'claimed',
    idempotency_key varchar(128) not null,
    claimed_at timestamptz not null default now(),
    redeemed_order_id bigint,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint ck_coupon_claim_status check (status in ('claimed', 'redeemed', 'expired')),
    constraint ck_coupon_claim_idempotency check (char_length(idempotency_key) between 8 and 128)
);
create unique index if not exists uk_coupon_claim_idempotency on coupon_claim(user_id, idempotency_key);
create index if not exists idx_coupon_claim_user on coupon_claim(user_id, status, claimed_at desc, id desc);
create index if not exists idx_coupon_claim_coupon on coupon_claim(coupon_id, user_id, status);
