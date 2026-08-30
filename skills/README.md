# XiaoLanHe Repo Skills

The repository owns one implementation workflow Skill:
`plugins/xiaolanhe/skills/xiaolanhe-feature-workflow/SKILL.md`.

Sync it into the shared agent skill directory after checkout or pull:

```bash
python3 skills/plugins/xiaolanhe/lib/agents/sync_repo_skills.py --prune
```

The script only prunes names recorded in its own lock file. The checked-in
Skill remains the source of truth.
