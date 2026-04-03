# Feature Development Pipeline

Agent-driven feature development: Notion task → agent develops → preview deploy → test → report.

## Flow

```
Notion (status → "Ready")
  → webhook → GitLab CI trigger
    → understand stage (read task, clarify or plan)
    → develop stage (branch, code, unit tests)
    → deploy-preview stage (vd push + deploy)
    → test stage (UI tests via agent skill)
    → report stage (results to Notion, create MR)
```

## Notion Task Structure

Required fields in Notion database:

| Field | Type | Description |
|-------|------|-------------|
| Project | Select | Maps to GitLab repo URL |
| Description | Text | Free text, user's language |
| Acceptance | Text | What "done" looks like |
| Credentials | Text | API keys needed, or "none" |
| Status | Select | State machine (see below) |

### Status state machine

```
Backlog → Ready → In Progress → Needs Clarification → Ready (loop)
                → In Progress → Preview Ready → Tests Passed / Tests Failed
                → Review → Done
```

- Only human-triggered transitions: "Backlog → Ready" and "Needs Clarification → Ready"
- Everything else is agent-driven via Notion API

## Notion → GitLab Trigger

Notion automation: when status becomes "Ready" → POST to GitLab pipeline trigger API.

Passes variables:
- `NOTION_PAGE_ID`
- `PROJECT` (repo URL, derived from the Project select field)

### Webhook bridge options

1. Notion native automations (limited but may suffice)
2. n8n/Make as middleware
3. **Small app on vd itself** — dogfooding. Receives Notion webhook, calls GitLab trigger API

Option 3 is preferred — simple HTTP handler, deployed on the same platform.

## GitLab CI Pipeline

```yaml
stages:
  - understand
  - develop
  - deploy-preview
  - test
  - report

variables:
  NOTION_PAGE_ID: $NOTION_PAGE_ID
  PREVIEW_NAME: "preview-${CI_PIPELINE_ID}"
  VD_SSH: "ssh -i $VD_KEY -o StrictHostKeyChecking=accept-new vd-user@$VD_HOST"

understand:
  stage: understand
  timeout: 5 minutes
  script:
    # Read task from Notion + project CLAUDE.md
    # If unclear → write questions to Notion, set "Needs Clarification", exit
    # If clear → write plan to Notion, proceed
    - claude-code --print "read notion task, decide if clear or needs clarification"

develop:
  stage: develop
  timeout: 30 minutes
  script:
    - git checkout -b preview/$PREVIEW_NAME
    - claude-code --max-turns 50 "implement task, write tests, iterate until green"
    - git push origin preview/$PREVIEW_NAME

deploy-preview:
  stage: deploy-preview
  script:
    - tar cf - --exclude='node_modules' --exclude='.git'
        --exclude='__pycache__' --exclude='.venv'
        --exclude='venv' --exclude='.next' .
        | $VD_SSH "vd push $PREVIEW_NAME --json"
    - $VD_SSH "vd deploy /opt/vibe-deploy/push/$PREVIEW_NAME
        --name $PREVIEW_NAME --db postgres --json"

test:
  stage: test
  script:
    # UI tests via existing agent skill against preview URL
    - claude-code "/ui-test https://$PREVIEW_NAME.apps.platform.xaidos.com
        --creds $TEST_CREDS --context 'notion task $NOTION_PAGE_ID'"

report:
  stage: report
  when: always
  script:
    # Write results back to Notion, create MR if tests pass
    - claude-code "report results to notion, create MR if green"
```

## Guardrails

### Cost/time limits
- `timeout` per stage (5min understand, 30min develop, 10min test)
- `--max-turns` for Claude Code to cap token usage
- Diff size limit — fail if agent produces >2000 lines of changes

### Clarification bounds
- Max 1 clarification round
- After that, agent makes best-guess decisions and documents assumptions
- Clarification only for blocking ambiguity (missing project, contradictions)
- Not for aesthetic choices — agent decides and notes it

### Scope lock
- Agent only touches files relevant to the task
- No unrelated refactoring
- Tests written first, then implementation

## Preview Environments

- Name: `preview-{pipeline-id}` or `preview-{task-id}`
- URL: `https://preview-{id}.apps.platform.xaidos.com`
- Each gets its own PostgreSQL database
- Parallel previews don't conflict (separate containers, separate DBs)

### Cleanup

Two mechanisms:
1. **On MR merge/close** — GitLab webhook triggers `vd destroy $PREVIEW_NAME --yes --drop-db`
2. **TTL-based** — server cron destroys previews older than 48h (catches orphans)

## Reporting

### Success report (written to Notion):
```
Preview: https://preview-abc-123.apps.platform.xaidos.com
Unit tests: 12 passed
UI tests: 5 passed
Assumptions made:
  - Used blue (#2563EB) for primary button
  - Pagination at 20 items per page
Merge request: gitlab.com/project/-/merge_requests/45
```

### Failure report:
```
Failed at: develop stage
Tests failing: test_user_login (expected 200, got 401)
Agent tried 3 fix iterations, couldn't resolve
Likely issue: missing AUTH_SECRET env var
```

## Security

- `VD_KEY` and `ANTHROPIC_API_KEY` stored as GitLab CI secrets
- Notion API token scoped to task database only
- Consider enforcing `preview-` prefix in vd for CI-deployed apps
- Agent never commits secrets — `.env` passed via `--env-file`

## Two development modes

1. **Feature in existing project** — branch off, modify, deploy
2. **New app from scratch** — agent uses `/vibe` skill to design, scaffold, deploy

Agent infers from whether a Project is linked in the Notion task.

## Build order

1. Webhook bridge (Notion → GitLab trigger) — tiny app on vd
2. GitLab CI template — `.gitlab-ci.yml` with stages
3. Notion database template — structured fields + automation rule
4. Agent prompts — understand/develop/report prompt engineering
5. Cleanup hook — MR merge → destroy preview
