package hashline_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/lapp-dev/lapp/pkg/hashline"
)

// hashVector is one entry from testdata/hash_vectors.json.
type hashVector struct {
	Input        string `json:"input"`
	LineNum      int    `json:"lineNum"`
	ExpectedHash string `json:"expectedHash"`
}

// TestHashLine_Vectors verifies all ground-truth vectors from testdata.
func TestHashLine_Vectors(t *testing.T) {
	data, err := os.ReadFile("../../testdata/hash_vectors.json")
	if err != nil {
		t.Fatalf("cannot read test vectors: %v", err)
	}
	var vectors []hashVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("cannot parse test vectors: %v", err)
	}
	if len(vectors) == 0 {
		t.Fatal("no test vectors found")
	}
	for _, v := range vectors {
		got := hashline.HashLine(v.Input, v.LineNum)
		if got != v.ExpectedHash {
			t.Errorf("HashLine(%q, %d) = %q, want %q", v.Input, v.LineNum, got, v.ExpectedHash)
		}
	}
}

// TestHashLine_TrailingWhitespaceIgnored confirms that trailing spaces do not
// affect the hash — matching the "x := 1   " vector.
func TestHashLine_TrailingWhitespaceIgnored(t *testing.T) {
	a := hashline.HashLine("x := 1   ", 1)
	b := hashline.HashLine("x := 1", 1)
	if a != b {
		t.Errorf("trailing spaces changed hash: %q vs %q", a, b)
	}
}

// TestHashLine_StructuralPositionSensitive confirms that blank/structural lines
// (those without any letter or digit) produce different hashes at different
// line numbers, because the line number is used as the seed.
func TestHashLine_StructuralPositionSensitive(t *testing.T) {
	a := hashline.HashLine("}", 7)
	b := hashline.HashLine("}", 18)
	if a == b {
		t.Errorf("expected '}' at line 7 and line 18 to hash differently, both = %q", a)
	}
}

// TestHashLine_ContentPositionIndependent confirms that content lines (those
// containing at least one letter or digit) hash identically regardless of
// line number, because the seed is always 0.
func TestHashLine_ContentPositionIndependent(t *testing.T) {
	a := hashline.HashLine("x := 1", 3)
	b := hashline.HashLine("x := 1", 15)
	if a != b {
		t.Errorf("same content at different line numbers should hash equally: %q vs %q", a, b)
	}
}

// TestHashLine_BOMExcluded documents the contract between fileio and HashLine:
// HashLine itself does NOT strip byte-order marks. The fileio layer MUST strip
// the BOM before calling HashLine so that the BOM bytes don't corrupt the hash.
// This test verifies that a BOM-prefixed line produces a DIFFERENT hash than
// the clean line — proving that an un-stripped BOM would cause mismatches.
func TestHashLine_BOMExcluded(t *testing.T) {
	withBOM := hashline.HashLine("\xef\xbb\xbffunc main() {", 1)
	withoutBOM := hashline.HashLine("func main() {", 1)
	if withBOM == withoutBOM {
		t.Errorf("BOM bytes should alter the hash (fileio must strip BOM before calling HashLine): both = %q", withBOM)
	}
}

// TestParseRef_ValidNormal checks that well-formed N#XX refs parse correctly.
func TestParseRef_ValidNormal(t *testing.T) {
	cases := []struct {
		ref      string
		wantLine int
		wantHash string
	}{
		{"5#SN", 5, "SN"},
		{"10#KT", 10, "KT"},
		{"1#KH", 1, "KH"},
	}
	for _, c := range cases {
		lineNum, hash, err := hashline.ParseRef(c.ref)
		if err != nil {
			t.Errorf("ParseRef(%q) unexpected error: %v", c.ref, err)
			continue
		}
		if lineNum != c.wantLine || hash != c.wantHash {
			t.Errorf("ParseRef(%q) = (%d, %q), want (%d, %q)", c.ref, lineNum, hash, c.wantLine, c.wantHash)
		}
	}
}

// TestParseRef_ValidBOF checks that the BOF sentinel "0:" is accepted.
func TestParseRef_ValidBOF(t *testing.T) {
	lineNum, hash, err := hashline.ParseRef("0:")
	if err != nil {
		t.Fatalf("ParseRef(\"0:\") unexpected error: %v", err)
	}
	if lineNum != 0 || hash != "" {
		t.Errorf("ParseRef(\"0:\") = (%d, %q), want (0, \"\")", lineNum, hash)
	}
}

// TestParseRef_ValidEOF checks that the EOF sentinel "EOF:" is accepted.
func TestParseRef_ValidEOF(t *testing.T) {
	lineNum, hash, err := hashline.ParseRef("EOF:")
	if err != nil {
		t.Fatalf("ParseRef(\"EOF:\") unexpected error: %v", err)
	}
	if lineNum != -1 || hash != "" {
		t.Errorf("ParseRef(\"EOF:\") = (%d, %q), want (-1, \"\")", lineNum, hash)
	}
}

// TestParseRef_Rejects verifies that malformed refs are all rejected with an error.
func TestParseRef_Rejects(t *testing.T) {
	bad := []string{
		"abc#ZZ",  // non-numeric line number
		"5#zz",   // lowercase hash chars
		"5",      // no '#' and not a sentinel
		"",       // empty string
		"5#Z",    // hash too short (1 char)
		"5#ZZZ",  // hash too long (3 chars)
		"0#ZZ",   // zero line number with hash (only "0:" is valid)
		"-1:",    // negative number (not a valid sentinel)
	}
	for _, ref := range bad {
		_, _, err := hashline.ParseRef(ref)
		if err == nil {
			t.Errorf("ParseRef(%q) should have returned an error but did not", ref)
		}
	}
}
