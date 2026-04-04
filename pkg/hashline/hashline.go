package hashline

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/OneOfOne/xxhash"
)

// Alphabet is the 16-character encoding alphabet used to encode the 8-bit hash value
// as two characters (high nibble first, low nibble second).
const Alphabet = "ZPMQVRWSNKTXJBYH"

// HashLine computes a 2-character hash for a single source line.
//
// Algorithm:
//  1. Strip all \r characters.
//  2. Trim trailing whitespace (' ', '\t', '\n').
//  3. If the resulting string contains any letter or digit, the hash is
//     content-only (seed=0); otherwise the line is structural/blank and the
//     seed is the 1-based line number so that identical structural tokens at
//     different positions produce different hashes.
//  4. Hash with xxhash32, take the low byte, encode via Alphabet.
func HashLine(line string, lineNum int) string {
	// Step 1: strip carriage returns (handles CRLF files)
	processed := strings.ReplaceAll(line, "\r", "")
	// Step 2: trim trailing whitespace
	processed = strings.TrimRight(processed, " \t\n")

	// Step 3: determine seed
	hasAlpha := false
	for _, r := range processed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasAlpha = true
			break
		}
	}
	seed := lineNum
	if hasAlpha {
		seed = 0
	}

	// Step 4-6: hash and encode
	h := xxhash.Checksum32S([]byte(processed), uint32(seed))
	b := byte(h & 0xFF)
	return string([]byte{Alphabet[b>>4], Alphabet[b&0x0F]})
}

// FormatLine returns the hashline-formatted representation of a line:
// "LINENUM#HASH:original_line"
func FormatLine(line string, lineNum int) string {
	return fmt.Sprintf("%d#%s:%s", lineNum, HashLine(line, lineNum), line)
}

// ParseRef parses a hashline reference string into its components.
//
// Valid forms:
//   - "0:"    → (0,  "",   nil) — BOF anchor; callers skip hash verification when lineNum==0
//   - "EOF:"  → (-1, "",   nil) — EOF anchor; callers skip hash verification when lineNum==-1
//   - "N#XX"  → (N,  "XX", nil) — normal ref; N is a positive integer, XX are two Alphabet chars
//
// All other forms return an error.
func ParseRef(ref string) (lineNum int, hash string, err error) {
	switch ref {
	case "0:":
		return 0, "", nil
	case "EOF:":
		return -1, "", nil
	}

	hashIdx := strings.Index(ref, "#")
	if hashIdx < 0 {
		return 0, "", fmt.Errorf("invalid ref %q: expected N#XX, \"0:\", or \"EOF:\"", ref)
	}

	numStr := ref[:hashIdx]
	hashStr := ref[hashIdx+1:]

	n, e := strconv.Atoi(numStr)
	if e != nil || n <= 0 {
		return 0, "", fmt.Errorf("invalid ref %q: line number must be a positive integer", ref)
	}

	if len(hashStr) != 2 {
		return 0, "", fmt.Errorf("invalid ref %q: hash must be exactly 2 Alphabet characters, got %d", ref, len(hashStr))
	}
	for _, c := range hashStr {
		if !strings.ContainsRune(Alphabet, c) {
			return 0, "", fmt.Errorf("invalid ref %q: hash character %q is not in Alphabet", ref, c)
		}
	}

	return n, hashStr, nil
}

// VerifyRef checks that the hash embedded in ref matches the actual content of
// the referenced line. BOF ("0:") and EOF ("EOF:") anchors always pass.
func VerifyRef(ref string, lines []string) error {
	lineNum, hash, err := ParseRef(ref)
	if err != nil {
		return err
	}
	// Special anchors — no content to verify.
	if lineNum == 0 || lineNum == -1 {
		return nil
	}
	if lineNum < 1 || lineNum > len(lines) {
		return fmt.Errorf("ref %q: line %d is out of range (file has %d lines)", ref, lineNum, len(lines))
	}
	computed := HashLine(lines[lineNum-1], lineNum)
	if computed != hash {
		return fmt.Errorf("ref %q: hash mismatch at line %d (stored=%s, computed=%s)", ref, lineNum, hash, computed)
	}
	return nil
}
