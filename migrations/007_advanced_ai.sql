alter table conversation_session
    add column if not exists summary_text text,
    add column if not exists summary_through_message_id bigint,
    add column if not exists summary_prompt_version varchar(64),
    add column if not exists summary_updated_at timestamptz;

update conversation_session
   set summary_text = nullif(btrim(metadata->>'summary_text'), '')
 where summary_text is null
   and nullif(btrim(metadata->>'summary_text'), '') is not null;

do $$ begin
    alter table conversation_session
        add constraint fk_conversation_summary_message
        foreign key (summary_through_message_id)
        references conversation_message(id) on delete set null;
exception when duplicate_object then null;
end $$;

do $$ begin
    alter table conversation_session
        add constraint ck_conversation_summary_prompt_version
        check (
            summary_prompt_version is null
            or summary_prompt_version ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$'
        );
exception when duplicate_object then null;
end $$;

do $$
declare
    duplicate_user_id bigint;
begin
    select user_id
      into duplicate_user_id
      from player_profile
     where user_id is not null
     group by user_id
    having count(*) > 1
     order by user_id
     limit 1;

    if duplicate_user_id is not null then
        raise exception 'migration 007 cannot create one-profile-per-user constraint: duplicate player_profile rows for user_id=%', duplicate_user_id;
    end if;
end $$;

create unique index if not exists uk_player_profile_user
    on player_profile(user_id)
    where user_id is not null;
