#!/usr/bin/env bash
# setup-github-pipeline.sh — one-time enforcement of the branching + deploy
# rules on GitHub. Run locally with the `gh` CLI authenticated as an org admin:
#
#   gh auth login
#   REVIEWER=<your-github-login> ./scripts/setup-github-pipeline.sh
#
# It is idempotent — safe to re-run. It configures:
#   1. Branch protection on `main` and `dev` (require green CI + a PR review,
#      block direct pushes and force-pushes).
#   2. A `staging` environment (no gate — auto-deploys).
#   3. A `production` environment WITH a required reviewer (the manual approval
#      gate that pauses the prod deploy until you click Approve).
set -euo pipefail

REPO="${REPO:-Startup2Team/Rides-api}"
REVIEWER="${REVIEWER:-}"           # your GitHub login; required for the prod gate
CHECKS='["Lint","Test","Docker Build"]'   # must match the job names in ci.yml

echo "==> Repo: $REPO"

protect_branch() {
  local branch="$1"
  echo "==> Protecting branch: $branch"
  gh api -X PUT "repos/$REPO/branches/$branch/protection" \
    -H "Accept: application/vnd.github+json" --input - <<JSON
{
  "required_status_checks": { "strict": true, "contexts": $CHECKS },
  "enforce_admins": false,
  "required_pull_request_reviews": {
    "required_approving_review_count": 1,
    "dismiss_stale_reviews": true
  },
  "restrictions": null,
  "required_linear_history": true,
  "allow_force_pushes": false,
  "allow_deletions": false
}
JSON
}

protect_branch main
protect_branch dev

echo "==> Creating 'staging' environment (auto-deploy, no gate)"
gh api -X PUT "repos/$REPO/environments/staging" >/dev/null

echo "==> Creating 'production' environment (manual approval gate)"
if [ -z "$REVIEWER" ]; then
  echo "    !! REVIEWER not set — creating production WITHOUT a required reviewer."
  echo "       Re-run with REVIEWER=<login> to add the approval gate, or add it in"
  echo "       Settings → Environments → production → Required reviewers."
  gh api -X PUT "repos/$REPO/environments/production" >/dev/null
else
  REVIEWER_ID=$(gh api "users/$REVIEWER" -q .id)
  gh api -X PUT "repos/$REPO/environments/production" --input - >/dev/null <<JSON
{
  "wait_timer": 0,
  "reviewers": [{ "type": "User", "id": $REVIEWER_ID }],
  "deployment_branch_policy": { "protected_branches": true, "custom_branch_policies": false }
}
JSON
  echo "    production requires approval from: $REVIEWER"
fi

cat <<'DONE'

==> Done. Remaining manual steps (secrets — never scripted):
    • Settings → Secrets → Actions → add DEPLOY_SSH_KEY (private key for root@139.84.251.242)
    • The rides-api GHCR package is PUBLIC → the box needs NO docker login
      (only if you make it private later: docker login ghcr.io with a read:packages PAT)
    • On the box: docker network create rides-edge   (shared nginx↔staging network)
    • See docs/devops/PIPELINE.md for the full box + nginx setup.
DONE
