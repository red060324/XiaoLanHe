#!/usr/bin/env python3
"""Install the repository-owned XiaoLanHe skills into an agent skill root."""

from __future__ import annotations

import argparse
import hashlib
import json
import shutil
from pathlib import Path


SOURCE = Path(__file__).resolve().parents[2] / "skills"
LOCK = ".xiaolanhe_repo_skills.lock.json"


def digest(root: Path) -> str:
    value = hashlib.sha256()
    for path in sorted(p for p in root.rglob("*") if p.is_file()):
        value.update(str(path.relative_to(root)).encode())
        value.update(path.read_bytes())
    return value.hexdigest()


def skill_name(root: Path) -> str:
    for line in (root / "SKILL.md").read_text(encoding="utf-8").splitlines():
        if line.startswith("name:"):
            return line.split(":", 1)[1].strip()
    raise ValueError(f"missing skill name: {root}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--target-root", type=Path, default=Path.home() / ".agents" / "skills")
    parser.add_argument("--prune", action="store_true")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    skills = [(skill_name(path), path) for path in sorted(SOURCE.iterdir()) if (path / "SKILL.md").is_file()]
    if not skills:
        raise ValueError(f"no skills found under {SOURCE}")

    lock_path = args.target_root / LOCK
    previous = json.loads(lock_path.read_text()) if lock_path.exists() else {"managed_skills": []}
    managed = [name for name, _ in skills]

    for name, source in skills:
        target = args.target_root / name
        current = target.exists() and digest(source) == digest(target)
        status = "already-current" if current else ("would-install" if args.dry_run else "installed")
        if not args.dry_run and not current:
            if target.exists():
                shutil.rmtree(target)
            shutil.copytree(source, target)
        print(f"{status}: {name} -> {target}")

    if args.prune:
        for name in previous.get("managed_skills", []):
            if name in managed:
                continue
            target = args.target_root / name
            if target.exists() and not args.dry_run:
                shutil.rmtree(target)
            print(f"{'would-prune' if args.dry_run else 'pruned'}: {name} -> {target}")

    if not args.dry_run:
        args.target_root.mkdir(parents=True, exist_ok=True)
        lock_path.write_text(json.dumps({"managed_skills": managed}, indent=2) + "\n")
        print(f"Lock file: {lock_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
