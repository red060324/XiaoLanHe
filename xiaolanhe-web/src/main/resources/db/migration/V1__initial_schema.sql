create extension if not exists vector;

create table if not exists user_account (
    id bigserial primary key,
    user_name varchar(128) not null,
    display_name varchar(128),
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create unique index if not exists uk_user_account_name on user_account(user_name);

create table if not exists player_profile (
    id bigserial primary key,
    user_id bigint references user_account(id),
    default_game varchar(64),
    default_region varchar(32),
    preferences jsonb not null default '{}'::jsonb,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create table if not exists conversation_session (
    id bigserial primary key,
    session_key varchar(64) not null,
    user_id bigint references user_account(id),
    title varchar(255),
    game_code varchar(64),
    region_code varchar(32),
    metadata jsonb not null default '{}'::jsonb,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create unique index if not exists uk_conversation_session_key on conversation_session(session_key);

create table if not exists conversation_message (
    id bigserial primary key,
    session_id bigint not null references conversation_session(id) on delete cascade,
    role varchar(32) not null,
    content text not null,
    model_name varchar(128),
    metadata jsonb not null default '{}'::jsonb,
    created_at timestamptz not null default now()
);

create index if not exists idx_conversation_message_session on conversation_message(session_id, created_at);

create table if not exists knowledge_document (
    id bigserial primary key,
    source_type varchar(32) not null,
    title varchar(512) not null,
    source_url text,
    patch_version varchar(64),
    metadata jsonb not null default '{}'::jsonb,
    content_text text,
    published_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create table if not exists knowledge_chunk (
    id bigserial primary key,
    document_id bigint not null references knowledge_document(id) on delete cascade,
    chunk_no integer not null,
    chunk_text text not null,
    embedding vector(1536),
    metadata jsonb not null default '{}'::jsonb,
    created_at timestamptz not null default now()
);

create unique index if not exists uk_knowledge_chunk_document_no on knowledge_chunk(document_id, chunk_no);
create index if not exists idx_knowledge_chunk_embedding on knowledge_chunk using hnsw (embedding vector_cosine_ops);

create table if not exists tool_call_log (
    id bigserial primary key,
    session_key varchar(64),
    tool_name varchar(128) not null,
    request_json jsonb not null default '{}'::jsonb,
    response_json jsonb not null default '{}'::jsonb,
    success boolean not null default true,
    error_message text,
    latency_ms integer,
    created_at timestamptz not null default now()
);

create index if not exists idx_tool_call_log_session on tool_call_log(session_key, created_at);