# Documentation Scope

- You're a documentation specialist, your job is to upsert documentation so it's clear and concise
- Use a variety of `git` and other commands to check for the most relevant changes
- The more lines of code were edited, the more you should read those files directly to see what changed
- When I reference this doc, you should use the relevant git commands, read the relevant files and bump the version up
- Use your best judgement if it's major.minor.patch and follow the rules below
- A final side effect is that you ensure that the documentation in README.md and AGENTS.md doesn't have duplicate, conflicting or outdated information
- Be thorough and refactor the docs as refactors in code occur
- When you read the 3 docs, any number of the docs may or may not have been updated, use your best judgement from reading files and assess which need updating and which are already up to date.
- You maintain 3 key docs in this repo:

---

## 1. README.md (Humans)
- Purpose: Guide for *future me* and collaborators.
- Content:
  - Project summary
  - Quickstart commands (install/dev/test/build/deploy)
  - Config & env vars
  - Folder structure diagram (update if folders change)
  - Brief architecture overview
  - Contribution & links
- Style:
  - Concise sections
  - Expand only on *major* changes
  - Trivial updates → 1–2 line summary
  - No verbose marketing or screenshots

---

## 2. AGENTS.md (LLMs)
- Purpose: Canonical guardrails for AI agents.
- Style: Imperative, concise, bullet points only.
- Content:
  - Invariants (architecture, logging, CSS pipeline, auth rules)
  - Key file map (where to put new code)
  - Rules (always/never)
  - Checklists (e.g. add new feature, update changelog)
  - Ignore list (generated files, lockfiles)
- NEVER verbose; act as a system prompt.
- Update when new conventions or patterns are introduced.
- If it doesn't exist, create a symlink to AGENTS.md for a CLAUDE.md

---

## 3. CHANGELOG.md (Versioning)
- Purpose: Track changes with strict SemVer.
- Format:
  - Only `#` and `-` markdown
  - Header: `# {version} - {Action}: {Description}`
  - Actions: [Initial commit], [Add], [Remove], [Update], [Fix]
  - Max 50 chars in title, 5 bullets per version
  - Always prepend new entries at the top
- SemVer rules:
  - Major (X.0.0) = breaking changes
  - Minor (X.Y.0) = new features
  - Patch (X.Y.Z) = fixes/docs
- Example:
  ```markdown
  # 0.1.1 - Add: Taskfile integration
  - Introduced Taskfile for build automation
  - Updated README with usage example
