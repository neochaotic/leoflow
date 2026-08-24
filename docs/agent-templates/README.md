# Agent Context Template

This directory holds **templates** for AI coding agent context files. These templates ARE committed to the repository.

The actual files used by agents (e.g., `CLAUDE.md` at the repo root) are listed in `.gitignore` and not committed. The reason: each developer may evolve their agent guidance independently, and we do not want every personal tweak to enter the shared history.

## How to Use

When you clone the repo and want to work with Claude Code:

```bash
cp docs/agent-templates/CLAUDE.md.template ./CLAUDE.md
```

Now Claude Code will pick up `CLAUDE.md` from the repo root. Modify it locally as needed; it will not be committed.

The same pattern applies for other agents:

```bash
cp docs/agent-templates/AGENTS.md.template ./AGENTS.md      # for Codex, etc.
cp docs/agent-templates/GEMINI.md.template ./GEMINI.md      # for Gemini CLI
```

## What Goes in a Template

- Project context (what the codebase is)
- Architectural principles (non-negotiables)
- Tech stack
- Conventions
- Where to find ADRs
- What's out of scope

## What Does NOT Go in a Template

- Personal preferences for verbosity
- Local environment specifics
- Credentials, tokens, or paths from a developer's machine

Templates are the **starting point**. Personal `CLAUDE.md` files (gitignored) are where you customize.
