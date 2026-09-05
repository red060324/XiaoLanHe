alter table purchase_order
    add column if not exists source_type varchar(24) not null default 'standard',
    add column if not exists source_reference varchar(64),
    add column if not exists payment_expires_at timestamptz;

do $$ begin
    alter table purchase_order add constraint ck_purchase_order_source
        check (source_type in ('standard','flash_sale'));
exception when duplicate_object then null;
end $$;

do $$ begin
    alter table purchase_order add constraint ck_purchase_order_source_reference
        check ((source_type='standard' and source_reference is null and payment_expires_at is null)
            or (source_type='flash_sale' and source_reference is not null and payment_expires_at is not null));
exception when duplicate_object then null;
end $$;

create unique index if not exists uk_purchase_order_source
    on purchase_order(source_type,source_reference) where source_reference is not null;
create index if not exists idx_purchase_order_flash_expiry
    on purchase_order(payment_expires_at,id) where source_type='flash_sale' and status='pending_payment';

create table if not exists flash_sale_activity (
    id bigserial primary key,
    code varchar(64) not null,
    edition_id bigint not null references game_edition(id),
    region_code varchar(16) not null,
    currency char(3) not null,
    sale_price_minor bigint not null,
    total_stock bigint not null,
    allocated_stock bigint not null default 0,
    status varchar(16) not null default 'draft',
    starts_at timestamptz not null,
    ends_at timestamptz not null,
    payment_timeout_seconds integer not null default 900,
    version bigint not null default 0,
    created_by bigint not null references user_account(id),
    activated_at timestamptz,
    cancelled_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint ck_flash_sale_activity_code check (char_length(code) between 3 and 64),
    constraint ck_flash_sale_activity_region check (char_length(region_code) between 2 and 16),
    constraint ck_flash_sale_activity_currency check (currency ~ '^[A-Z]{3}$'),
    constraint ck_flash_sale_activity_price check (sale_price_minor >= 0),
    constraint ck_flash_sale_activity_stock check (total_stock > 0 and allocated_stock between 0 and total_stock),
    constraint ck_flash_sale_activity_status check (status in ('draft','active','cancelled','ended')),
    constraint ck_flash_sale_activity_window check (starts_at < ends_at),
    constraint ck_flash_sale_activity_timeout check (payment_timeout_seconds between 60 and 86400),
    constraint ck_flash_sale_activity_version check (version >= 0)
);

create unique index if not exists uk_flash_sale_activity_code on flash_sale_activity(lower(code));
create index if not exists idx_flash_sale_activity_window on flash_sale_activity(status,starts_at,ends_at,id);
create index if not exists idx_flash_sale_activity_edition on flash_sale_activity(edition_id,created_at desc,id desc);

create table if not exists flash_sale_reservation (
    request_id varchar(64) primary key,
    activity_id bigint not null references flash_sale_activity(id),
    user_id bigint not null references user_account(id),
    idempotency_digest bytea not null,
    status varchar(16) not null default 'reserved',
    order_id bigint references purchase_order(id),
    failure_code varchar(48),
    reserved_at timestamptz not null,
    payment_expires_at timestamptz not null,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint ck_flash_sale_request_id check (request_id ~ '^fsr_[1-9a-z][0-9a-z]{0,12}_[a-f0-9]{32}$'),
    constraint ck_flash_sale_digest check (octet_length(idempotency_digest)=32),
    constraint ck_flash_sale_reservation_status check (status in ('reserved','order_ready','failed','expired')),
    constraint ck_flash_sale_reservation_order check (
        (status in ('reserved','failed') and order_id is null)
        or (status in ('order_ready','expired') and order_id is not null)
    )
);

create unique index if not exists uk_flash_sale_reservation_user
    on flash_sale_reservation(activity_id,user_id);
create unique index if not exists uk_flash_sale_reservation_digest
    on flash_sale_reservation(activity_id,user_id,idempotency_digest);
create unique index if not exists uk_flash_sale_reservation_order
    on flash_sale_reservation(order_id) where order_id is not null;
create index if not exists idx_flash_sale_reservation_owner
    on flash_sale_reservation(user_id,created_at desc,request_id);

create table if not exists flash_sale_release_job (
    id bigserial primary key,
    request_id varchar(64) not null,
    activity_id bigint not null references flash_sale_activity(id),
    user_id bigint not null references user_account(id),
    idempotency_digest bytea not null,
    reserved_at timestamptz not null,
    reason varchar(32) not null,
    status varchar(16) not null default 'pending',
    attempts integer not null default 0,
    next_attempt_at timestamptz not null default now(),
    lease_until timestamptz,
    last_error_code varchar(48),
    completed_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint uk_flash_sale_release_job_request unique (request_id),
    constraint ck_flash_sale_release_digest check (octet_length(idempotency_digest)=32),
    constraint ck_flash_sale_release_job_reason check (reason in ('technical_rollback','final_guard','payment_expired','admin_repair')),
    constraint ck_flash_sale_release_job_status check (status in ('pending','leased','done')),
    constraint ck_flash_sale_release_job_attempts check (attempts >= 0)
);

create index if not exists idx_flash_sale_release_job_due
    on flash_sale_release_job(status,next_attempt_at,id);
