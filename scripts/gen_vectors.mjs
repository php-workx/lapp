#!/usr/bin/env bun
// gen_vectors.mjs - generate oh-my-pi compatible hash test vectors
const NIBBLE_STR = "ZPMQVRWSNKTXJBYH";
const DICT = Array.from({ length: 256 }, (_, i) => {
  const h = i >>> 4; const l = i & 0x0f;
  return `${NIBBLE_STR[h]}${NIBBLE_STR[l]}`;
});
const RE_SIGNIFICANT = /[\p{L}\p{N}]/u;
function computeLineHash(idx, line) {
  line = line.replace(/\r/g, "").trimEnd();
  let seed = 0;
  if (!RE_SIGNIFICANT.test(line)) { seed = idx; }
  return DICT[Bun.hash.xxHash32(line, seed) & 0xff];
}

const cases = [
  // code lines
  { input: "func main() {",           lineNum: 1  },
  { input: "    return nil",           lineNum: 10 },
  { input: 'import "fmt"',            lineNum: 1  },
  // structural: `}` at different positions — must yield different hashes
  { input: "}",                        lineNum: 1  },
  { input: "}",                        lineNum: 5  },
  { input: "}",                        lineNum: 10 },
  { input: "}",                        lineNum: 50 },
  // structural: `{` at line 1
  { input: "{",                        lineNum: 1  },
  // blank / whitespace-only — structural, seed = lineNum
  { input: "",                         lineNum: 1  },
  { input: "   ",                      lineNum: 3  },
  { input: "\t",                       lineNum: 2  },
  // content lines: `x := 1` at multiple positions — must yield SAME hash
  { input: "x := 1",                  lineNum: 1  },
  { input: "x := 1",                  lineNum: 5  },
  { input: "x := 1",                  lineNum: 10 },
  // trailing whitespace must equal trimmed version
  { input: "x := 1   ",              lineNum: 1  },
  // mixed indentation
  { input: "\t    mixed",             lineNum: 4  },
  // unicode
  { input: "// 日本語コメント",        lineNum: 1  },
  { input: 'café := "ok"',           lineNum: 1  },
  // more code lines
  { input: "// simple comment",       lineNum: 5  },
  { input: "    if err != nil {",     lineNum: 6  },
  { input: '        return fmt.Errorf("missing ID")', lineNum: 3 },
];

const vectors = cases.map(({ input, lineNum }) => ({
  input,
  lineNum,
  expectedHash: computeLineHash(lineNum, input),
}));

import { writeFileSync } from 'fs';
writeFileSync('testdata/hash_vectors.json', JSON.stringify(vectors, null, 2) + '\n');
console.log(`Generated ${vectors.length} test vectors`);

// Sanity assertions
const h = (line, n) => computeLineHash(n, line);

// structural lines at different positions must differ
const hb1 = h("}", 1), hb5 = h("}", 5), hb10 = h("}", 10), hb50 = h("}", 50);
console.assert(hb1 !== hb5,  `FAIL: } at 1 and 5 should differ`);
console.assert(hb5 !== hb10, `FAIL: } at 5 and 10 should differ`);
console.assert(hb1 !== hb50, `FAIL: } at 1 and 50 should differ`);
console.log(`} hashes: ${hb1} ${hb5} ${hb10} ${hb50} — all position-sensitive`);

// content lines at different positions must be identical
const hx1 = h("x := 1", 1), hx5 = h("x := 1", 5), hx10 = h("x := 1", 10);
console.assert(hx1 === hx5,  `FAIL: x := 1 should match at pos 1 and 5`);
console.assert(hx5 === hx10, `FAIL: x := 1 should match at pos 5 and 10`);
console.log(`x := 1 hash: ${hx1} — position-independent`);

// trailing whitespace must equal trimmed
const hTrim = h("x := 1", 1), hTrail = h("x := 1   ", 1);
console.assert(hTrim === hTrail, `FAIL: trailing whitespace must strip`);
console.log(`trailing-ws hash match: ${hTrim === hTrail}`);
