alter table knowledge_document add column if not exists game_code varchar(64);
alter table knowledge_document add column if not exists region_code varchar(32);

create index if not exists idx_knowledge_document_game_region on knowledge_document(game_code, region_code);