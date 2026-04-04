---
id: lap-lsuv
status: closed
deps: []
links: []
created: 2026-04-04T04:16:22Z
type: task
priority: 2
assignee: Ronny Unger
tags: [wave-1, lapp]
---
# Issue 2: Phase 0 — oh-my-pi hash test vectors

Extract oh-my-pi's exact computeLineHash output before writing any Go hashing code. These vectors are the ground truth for all Phase 1 tests.

Clone or vendor oh-my-pi's computeLineHash from packages/coding-agent/src/patch/hashline.ts. Write a Bun/Node.js runner that calls it on the full input set from §14 Phase 0 and writes results to testdata/hash_vectors.json.

PRE-MORTEM FIX pm-20260404-001: If oh-my-pi GitHub repo is inaccessible or unlicensed, implement computeLineHash directly using xxhashjs npm package and the §7.1 algorithm:

  npm install xxhashjs
  const XXH = require('xxhashjs');
  const ALPHABET = "ZPMQVRWSNKTXJBYH";

  function computeLineHash(line, lineNum) {
    line = line.replace(/\r/g, "");
    line = line.trimEnd();
    const hasAlphanumeric = /[a-zA-Z0-9\u00C0-\uFFFF]/.test(line);
    const seed = hasAlphanumeric ? 0 : lineNum;
    const buf = Buffer.from(line, 'utf8');
    const hash = XXH.h32(buf, seed).toNumber();
    const b = hash & 0xFF;
    return ALPHABET[b >> 4] + ALPHABET[b & 0x0F];
  }

testdata/hash_vectors.json schema — each entry has exactly these fields:

  [
    {"input": "func main() {", "lineNum": 1, "expectedHash": "XX"},
    {"input": "}", "lineNum": 7, "expectedHash": "XX"},
    ...
  ]

Required input set from §14 Phase 0:
- func main() {, return nil, import "fmt" — code lines at line 1
- }, {, "" (empty), whitespace-only — structural lines
- } at lines 1, 5, 10, 50 — position sensitivity for structural
- x := 1 at lines 1, 5, 10 — position independence for content
- Lines with trailing spaces, tabs, mixed indentation
- // 日本語コメント, café := "ok" — Unicode content

## Acceptance Criteria

testdata/hash_vectors.json exists with ≥20 entries covering all input categories from §14 Phase 0

