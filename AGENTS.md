# vibe-deploy — Agent Instructions

When modifying vibe-deploy capabilities (new features, changed constraints, new app types, new commands), you MUST also update the corresponding skills so that agents building apps are aware of the changes.

## Skills to Update

The skills are in the `plugin/skills/` directory:

- **`plugin/skills/vibe/SKILL.md`** — Platform constraints + project audit. Update when:
  - New capabilities are added (e.g. Redis, new DB type)
  - Existing capabilities change (e.g. new app type, port defaults)
  - Constraints change (e.g. something unavailable becomes available)
  - New app patterns or rules for writing code
  - Audit checklist items change (new compatibility checks needed)

- **`plugin/skills/deploy/SKILL.md`** — Deployment commands reference. Update when:
  - New `vd` commands are added (e.g. `vd push`)
  - Command flags change (new flags, removed flags, changed defaults)
  - Deploy workflow changes (e.g. push + deploy flow)
  - Error codes are added or changed
  - SSH connection method changes

## Also Update

- **`README.md`** — User-facing docs. Update when skills, capabilities, setup, or CLI change.
- **`CLAUDE.md`** — Full platform reference for agents working on vd itself. Update when architecture, commands, or constraints change.

## What to Keep in Sync

| Change in vibe-deploy | Update in |
|----------------------|-----------|
| New `vd` command | `deploy/SKILL.md` + `CLAUDE.md` + `README.md` CLI reference |
| New `--flag` on existing command | `deploy/SKILL.md` flag table + `CLAUDE.md` |
| New app type supported | `vibe/SKILL.md` app types table + `CLAUDE.md` |
| New infrastructure (Redis, S3, etc.) | `vibe/SKILL.md` "Can Use" / "Cannot Use" + `CLAUDE.md` |
| Changed default port | `vibe/SKILL.md` ports table + `CLAUDE.md` |
| New error code | `deploy/SKILL.md` error codes + `CLAUDE.md` |
| Changed deploy flow | Both skills + `CLAUDE.md` + `README.md` |
| New audit check | `vibe/SKILL.md` "Inspect Existing Project" section |

## Rule

If you change code and don't update the skills, agents will build apps incorrectly or deploy with wrong commands. Always check all four files after any change.
