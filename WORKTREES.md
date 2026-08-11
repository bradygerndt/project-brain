# Git Worktree Guide for project-brain

## Branch convention

| Branch | Purpose |
|--------|---------|
| `main` | Stable, running in Docker |
| `dev`  | Integration branch — merge features here first |
| `feature/<name>` | One branch per feature or experiment |

## Working on a feature

```bash
# From the project-brain repo root:

# Add a worktree for a new feature (checked out into a sibling directory)
git worktree add ../project-brain-<feature> -b feature/<feature>

# Work in that directory
cd ../project-brain-<feature>
npm install   # worktrees share .git but have their own node_modules

# When done, merge to dev and remove the worktree
git checkout dev
git merge feature/<feature>
git worktree remove ../project-brain-<feature>
```

## Claude Code agent isolation

Claude Code automatically uses git worktrees for isolated agent work when
`isolation: "worktree"` is passed to the Agent tool. This repo is set up for
that — main stays clean and agents branch off it without interfering.

## List active worktrees

```bash
git worktree list
```
