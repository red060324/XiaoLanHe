alter table user_account add column if not exists password_hash varchar(60);
alter table user_account add column if not exists role varchar(16) not null default 'user';
alter table user_account add column if not exists status varchar(16) not null default 'active';

do $$ begin
    alter table user_account add constraint ck_user_account_role check (role in ('user', 'admin'));
exception when duplicate_object then null;
end $$;
do $$ begin
    alter table user_account add constraint ck_user_account_status check (status in ('active', 'disabled'));
exception when duplicate_object then null;
end $$;

create unique index if not exists uk_user_account_name_normalized on user_account(lower(user_name));

create table if not exists user_session (
    id bigserial primary key,
    user_id bigint not null references user_account(id) on delete cascade,
    token_hash char(64) not null,
    expires_at timestamptz not null,
    last_seen_at timestamptz not null default now(),
    revoked_at timestamptz,
    created_at timestamptz not null default now(),
    constraint ck_user_session_expiry check (expires_at > created_at)
);
create unique index if not exists uk_user_session_token_hash on user_session(token_hash);
create index if not exists idx_user_session_user_expiry on user_session(user_id, expires_at desc);

create table if not exists game (
    id bigserial primary key,
    slug varchar(64) not null,
    name varchar(160) not null,
    summary varchar(500) not null default '',
    description text not null default '',
    developer varchar(160) not null default '',
    publisher varchar(160) not null default '',
    release_date date,
    cover_url text not null default '',
    status varchar(16) not null default 'active',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint ck_game_status check (status in ('active', 'inactive'))
);
create unique index if not exists uk_game_slug on game(lower(slug));
create index if not exists idx_game_public on game(status, id desc);

create table if not exists game_edition (
    id bigserial primary key,
    game_id bigint not null references game(id) on delete cascade,
    code varchar(64) not null,
    name varchar(160) not null,
    description text not null default '',
    status varchar(16) not null default 'active',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint ck_game_edition_status check (status in ('active', 'inactive'))
);
create unique index if not exists uk_game_edition_code on game_edition(game_id, code);

create table if not exists game_price (
    id bigserial primary key,
    edition_id bigint not null references game_edition(id) on delete cascade,
    region_code varchar(16) not null,
    currency char(3) not null,
    amount_minor bigint not null,
    active_from timestamptz not null default now(),
    active_until timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint ck_game_price_amount check (amount_minor >= 0),
    constraint ck_game_price_window check (active_until is null or active_until > active_from)
);
create unique index if not exists uk_game_price_active_key on game_price(edition_id, region_code, currency, active_from);
create unique index if not exists uk_game_price_one_active on game_price(edition_id, region_code, currency) where active_until is null;
create index if not exists idx_game_price_lookup on game_price(edition_id, region_code, currency, active_from desc);

create table if not exists game_entitlement (
    id bigserial primary key,
    user_id bigint not null references user_account(id) on delete cascade,
    edition_id bigint not null references game_edition(id),
    source_order_id bigint,
    status varchar(16) not null default 'active',
    granted_at timestamptz not null default now(),
    created_at timestamptz not null default now(),
    constraint ck_game_entitlement_status check (status in ('active', 'revoked'))
);
create unique index if not exists uk_game_entitlement_owner on game_entitlement(user_id, edition_id) where status = 'active';
