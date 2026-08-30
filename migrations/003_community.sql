create table if not exists community_post (
    id bigserial primary key,
    author_id bigint not null references user_account(id),
    game_id bigint references game(id),
    title varchar(160) not null,
    content text not null,
    status varchar(16) not null default 'published',
    deleted_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint ck_community_post_status check (status in ('published', 'hidden', 'deleted')),
    constraint ck_community_post_deleted check ((status = 'deleted') = (deleted_at is not null)),
    constraint ck_community_post_title check (char_length(btrim(title)) between 1 and 160),
    constraint ck_community_post_content check (char_length(btrim(content)) between 1 and 10000)
);
create index if not exists idx_community_post_feed on community_post(created_at desc, id desc) where status = 'published';
create index if not exists idx_community_post_game_feed on community_post(game_id, created_at desc, id desc) where status = 'published';
create index if not exists idx_community_post_author on community_post(author_id, created_at desc, id desc);

create table if not exists community_comment (
    id bigserial primary key,
    post_id bigint not null references community_post(id),
    author_id bigint not null references user_account(id),
    content text not null,
    status varchar(16) not null default 'published',
    deleted_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint ck_community_comment_status check (status in ('published', 'hidden', 'deleted')),
    constraint ck_community_comment_deleted check ((status = 'deleted') = (deleted_at is not null)),
    constraint ck_community_comment_content check (char_length(btrim(content)) between 1 and 3000)
);
create index if not exists idx_community_comment_post on community_comment(post_id, created_at, id) where status = 'published';
create index if not exists idx_community_comment_author on community_comment(author_id, created_at desc, id desc);

create table if not exists community_reaction (
    id bigserial primary key,
    post_id bigint not null references community_post(id) on delete cascade,
    user_id bigint not null references user_account(id) on delete cascade,
    reaction_type varchar(16) not null,
    created_at timestamptz not null default now(),
    constraint ck_community_reaction_type check (reaction_type in ('like', 'helpful', 'funny')),
    constraint uk_community_reaction unique (post_id, user_id, reaction_type)
);
create index if not exists idx_community_reaction_post on community_reaction(post_id, reaction_type);
