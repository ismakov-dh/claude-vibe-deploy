# vibe-deploy — Agent Instructions

When modifying vibe-deploy capabilities (new features, changed constraints, new app types, new commands), you MUST also update the corresponding skills so that agents building apps are aware of the changes.

## Skills to Update

The skills are in the `skills/` directory at the repo root:

- **`skills/vibe/SKILL.md`** — Platform constraints loaded when building apps. Update when:
  - New capabilities are added (e.g. Redis, new DB type)
  - Existing capabilities change (e.g. new app type, port defaults)
  - Constraints change (e.g. something that was unavailable becomes available)
  - New app patterns or rules for writing code

- **`skills/deploy/SKILL.md`** — Deployment commands reference. Update when:
  - New `vd` commands are added
  - Command flags change (new flags, removed flags, changed defaults)
  - Deploy workflow changes
  - Error codes are added or changed
  - SSH connection method changes

## What to Keep in Sync

| Change in vibe-deploy | Update in skills |
|----------------------|-----------------|
| New `vd` command | `deploy/SKILL.md` command reference |
| New `--flag` on existing command | `deploy/SKILL.md` flag table |
| New app type supported | `vibe/SKILL.md` app types table + detection info |
| New infrastructure (Redis, S3, etc.) | `vibe/SKILL.md` "What You Can Use" + remove from "What You CANNOT Use" |
| Changed default port | `vibe/SKILL.md` ports table |
| New error code | `deploy/SKILL.md` error codes table |
| Changed deploy flow | Both skills |

## Rule

If you change code and don't update the skills, agents will build apps incorrectly or deploy with wrong commands. Always check both skills after any change.
