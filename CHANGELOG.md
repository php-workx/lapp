# Changelog

## v0.1.0

Initial release of lapp — a hashline-addressing MCP server for AI coding agents.

- **Hashline addressing**: Every line is tagged `LINE#HASH` so agents reference exact positions without reproducing surrounding code, reducing output tokens per edit
- **8 MCP tools**: `lapp_read`, `lapp_edit`, `lapp_write`, `lapp_grep`, `lapp_find_block`, `lapp_replace_block`, `lapp_insert_block`, `lapp_apply_patch` — covers single-line edits, multi-line block replacements, bulk patches, and search-and-edit workflows
- **Deterministic edits**: No LLM in the loop — all operations are verified via content hashes before writing, with stale-reference repair and self-correcting responses when references drift
- **Atomic writes**: All file mutations go through a temp-file-and-rename pipeline; crashes never corrupt the original file
- **Cross-platform file safety**: Advisory file locking (flock/LockFileEx), BOM preservation, CRLF/mixed-ending handling, binary detection, UTF-8 validation, symlink resolution, and path security with a configurable block list
- **Multi-agent support**: Works with Claude Code, Codex, Cline, and any MCP-compatible agent via stdio transport
- **Configurable access control**: `--root` scoping, `--block`/`--allow` path patterns, `--only-tools` allow-list, and `--limit` line caps