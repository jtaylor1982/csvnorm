package main

import (
	"bufio"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestResolveDelimExplicit(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("a,b,c\n"))
	got, err := resolveDelim(r, ";")
	if err != nil {
		t.Fatalf("resolveDelim: %v", err)
	}
	if got != ';' {
		t.Errorf("got delim %q, want ';'", got)
	}
}

func TestResolveDelimExplicitRejectsMultiChar(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("a,b,c\n"))
	if _, err := resolveDelim(r, "::"); err == nil {
		t.Error("expected error for multi-character delimiter, got nil")
	}
}

func TestResolveDelimSniffs(t *testing.T) {
	cases := []struct {
		name string
		line string
		want rune
	}{
		{"comma", "name,age,city\n", ','},
		{"semicolon", "name;age;city\n", ';'},
		{"tab", "name\tage\tcity\n", '\t'},
		{"pipe", "name|age|city\n", '|'},
		{"comma wins ties", "a,b;c\n", ','},
		{"only first line counts", "a;b;c\nd,e,f,g,h,i\n", ';'},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(c.line))
			got, err := resolveDelim(r, "")
			if err != nil {
				t.Fatalf("resolveDelim: %v", err)
			}
			if got != c.want {
				t.Errorf("got delim %q, want %q", got, c.want)
			}
		})
	}
}

func TestResolveDelimNoCandidatesFallsBackToComma(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("abcdef\n"))
	got, err := resolveDelim(r, "")
	if err != nil {
		t.Fatalf("resolveDelim: %v", err)
	}
	if got != ',' {
		t.Errorf("got delim %q, want ','", got)
	}
}

func TestParseDelimChar(t *testing.T) {
	got, err := parseDelimChar(";")
	if err != nil {
		t.Fatalf("parseDelimChar: %v", err)
	}
	if got != ';' {
		t.Errorf("got %q, want ';'", got)
	}
}

func TestParseDelimCharRejectsMultiChar(t *testing.T) {
	if _, err := parseDelimChar("::"); err == nil {
		t.Error("expected error for multi-character delimiter, got nil")
	}
}

func TestParseDelimCharRejectsEmpty(t *testing.T) {
	if _, err := parseDelimChar(""); err == nil {
		t.Error("expected error for empty delimiter, got nil")
	}
}

func TestStripLeadingBOM(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(bom + "a,b,c\n"))
	stripLeadingBOM(r)
	rest, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString: %v", err)
	}
	if rest != "a,b,c\n" {
		t.Errorf("got %q after stripping BOM, want %q", rest, "a,b,c\n")
	}
}

func TestStripLeadingBOMAbsent(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("a,b,c\n"))
	stripLeadingBOM(r)
	rest, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString: %v", err)
	}
	if rest != "a,b,c\n" {
		t.Errorf("got %q, want %q", rest, "a,b,c\n")
	}
}

func TestNormalizeRowPadsShortRows(t *testing.T) {
	got := normalizeRow([]string{"a", "b"}, 4, false)
	want := []string{"a", "b", "", ""}
	if !equalRows(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNormalizeRowTruncatesLongRows(t *testing.T) {
	got := normalizeRow([]string{"a", "b", "c", "d"}, 2, false)
	want := []string{"a", "b"}
	if !equalRows(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNormalizeRowExactWidthUnchanged(t *testing.T) {
	got := normalizeRow([]string{"a", "b", "c"}, 3, false)
	want := []string{"a", "b", "c"}
	if !equalRows(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNormalizeRowTrims(t *testing.T) {
	got := normalizeRow([]string{" a ", "b\t", " c"}, 3, true)
	want := []string{"a", "b", "c"}
	if !equalRows(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRowIsEmpty(t *testing.T) {
	if !rowIsEmpty([]string{"", "", ""}) {
		t.Error("expected all-empty row to be empty")
	}
	if rowIsEmpty([]string{"", "x", ""}) {
		t.Error("expected row with a non-empty field to not be empty")
	}
	if !rowIsEmpty(nil) {
		t.Error("expected nil row to be empty")
	}
}

func equalRows(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRunEndToEnd(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "messy.csv")
	outPath := filepath.Join(dir, "clean.csv")

	input := bom + "name; age ;city\nAlice ;30; Springfield\nBob;; Shelbyville\n ; ; \n"
	if err := os.WriteFile(inPath, []byte(input), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := run(inPath, outPath, "", ",", "", true, false, true); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := "name,age,city\nAlice,30,Springfield\nBob,,Shelbyville\n"
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestParseRecordsHandlesWellFormedEmbeddedNewline(t *testing.T) {
	data := []byte("name,note\nAlice,\"line one\nline two\"\nBob,fine\n")
	records, fellBack, err := parseRecords(data, ',')
	if err != nil {
		t.Fatalf("parseRecords: %v", err)
	}
	if fellBack {
		t.Error("expected no fallback for a properly closed quoted field")
	}
	want := [][]string{
		{"name", "note"},
		{"Alice", "line one\nline two"},
		{"Bob", "fine"},
	}
	if len(records) != len(want) {
		t.Fatalf("got %d records, want %d: %v", len(records), len(want), records)
	}
	for i := range want {
		if !equalRows(records[i], want[i]) {
			t.Errorf("record %d: got %v, want %v", i, records[i], want[i])
		}
	}
}

func TestParseRecordsFallsBackOnUnterminatedQuote(t *testing.T) {
	data := []byte("name,note\nAlice,\"unterminated note\nBob,fine\n")
	records, fellBack, err := parseRecords(data, ',')
	if err != nil {
		t.Fatalf("parseRecords: %v", err)
	}
	if !fellBack {
		t.Fatal("expected fallback for an unterminated quoted field")
	}
	want := [][]string{
		{"name", "note"},
		{"Alice", "unterminated note"},
		{"Bob", "fine"},
	}
	if len(records) != len(want) {
		t.Fatalf("got %d records, want %d: %v", len(records), len(want), records)
	}
	for i := range want {
		if !equalRows(records[i], want[i]) {
			t.Errorf("record %d: got %v, want %v", i, records[i], want[i])
		}
	}
}

func TestSplitLinePermissiveHonorsQuotedDelimiter(t *testing.T) {
	got := splitLinePermissive(`"Smith, John",30,"NYC"`, ',')
	want := []string{"Smith, John", "30", "NYC"}
	if !equalRows(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSplitLinePermissiveUnbalancedQuoteKeepsRestOfLine(t *testing.T) {
	got := splitLinePermissive(`He said "hi,30`, ',')
	want := []string{"He said hi,30"}
	if !equalRows(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRunHandlesUnterminatedQuote(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "broken.csv")
	outPath := filepath.Join(dir, "clean.csv")

	input := "name,note\nAlice,\"unterminated note\nBob,fine\n"
	if err := os.WriteFile(inPath, []byte(input), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := run(inPath, outPath, "", ",", "", true, false, true); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := "name,note\nAlice,unterminated note\nBob,fine\n"
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunStrictRejectsRaggedRows(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "ragged.csv")
	outPath := filepath.Join(dir, "clean.csv")

	if err := os.WriteFile(inPath, []byte("a,b,c\nd,e\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := run(inPath, outPath, "", ",", "", true, true, true); err == nil {
		t.Error("expected error for ragged row under -strict, got nil")
	}
}

func TestRunCustomOutDelim(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "messy.csv")
	outPath := filepath.Join(dir, "clean.csv")

	input := "name,age,city\nAlice,30,Springfield\n"
	if err := os.WriteFile(inPath, []byte(input), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := run(inPath, outPath, "", ";", "", true, false, true); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := "name;age;city\nAlice;30;Springfield\n"
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunRejectsMultiCharOutDelim(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "in.csv")
	outPath := filepath.Join(dir, "out.csv")

	if err := os.WriteFile(inPath, []byte("a,b,c\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := run(inPath, outPath, "", "::", "", true, false, true); err == nil {
		t.Error("expected error for multi-character output delimiter, got nil")
	}
}

func TestDecodeToUTF8PassesThroughUTF8(t *testing.T) {
	for _, enc := range []string{"", "utf8", "UTF-8"} {
		got, err := decodeToUTF8([]byte("café,30\n"), enc)
		if err != nil {
			t.Fatalf("decodeToUTF8(%q): %v", enc, err)
		}
		if string(got) != "café,30\n" {
			t.Errorf("decodeToUTF8(%q): got %q, want %q", enc, got, "café,30\n")
		}
	}
}

func TestDecodeToUTF8RejectsUnknownEncoding(t *testing.T) {
	if _, err := decodeToUTF8([]byte("a,b\n"), "ebcdic"); err == nil {
		t.Error("expected error for unknown encoding, got nil")
	}
}

func TestLatin1ToUTF8(t *testing.T) {
	// 0xE9 is "é" in Latin-1.
	got := latin1ToUTF8([]byte{'c', 'a', 'f', 0xE9})
	if string(got) != "café" {
		t.Errorf("got %q, want %q", got, "café")
	}
}

func TestDecodeUTF16LittleEndianNoBOM(t *testing.T) {
	// "ab" as UTF-16LE code units.
	data := []byte{'a', 0, 'b', 0}
	got, err := decodeUTF16(data, binary.LittleEndian)
	if err != nil {
		t.Fatalf("decodeUTF16: %v", err)
	}
	if string(got) != "ab" {
		t.Errorf("got %q, want %q", got, "ab")
	}
}

func TestDecodeUTF16BigEndianNoBOM(t *testing.T) {
	data := []byte{0, 'a', 0, 'b'}
	got, err := decodeUTF16(data, binary.BigEndian)
	if err != nil {
		t.Fatalf("decodeUTF16: %v", err)
	}
	if string(got) != "ab" {
		t.Errorf("got %q, want %q", got, "ab")
	}
}

func TestDecodeUTF16BOMOverridesDefaultOrder(t *testing.T) {
	// LE BOM (FF FE) followed by "ab" in little-endian, but the caller
	// passes big-endian as the default; the BOM should win.
	data := []byte{0xFF, 0xFE, 'a', 0, 'b', 0}
	got, err := decodeUTF16(data, binary.BigEndian)
	if err != nil {
		t.Fatalf("decodeUTF16: %v", err)
	}
	if string(got) != "ab" {
		t.Errorf("got %q, want %q", got, "ab")
	}
}

func TestDecodeUTF16RejectsOddLength(t *testing.T) {
	if _, err := decodeUTF16([]byte{'a', 0, 'b'}, binary.LittleEndian); err == nil {
		t.Error("expected error for odd byte length, got nil")
	}
}

func TestRunDecodesLatin1Input(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "latin1.csv")
	outPath := filepath.Join(dir, "clean.csv")

	// "name,city\nJos\xe9,S\xe3o Paulo\n" -- Latin-1 bytes for accented chars.
	input := []byte("name,city\nJos\xe9,S\xe3o Paulo\n")
	if err := os.WriteFile(inPath, input, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := run(inPath, outPath, "", ",", "latin1", true, false, true); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := "name,city\nJosé,São Paulo\n"
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunDecodesUTF16Input(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "utf16.csv")
	outPath := filepath.Join(dir, "clean.csv")

	text := "name,age\nAlice,30\n"
	u16 := utf16.Encode([]rune(text))
	buf := make([]byte, 2+len(u16)*2)
	buf[0], buf[1] = 0xFF, 0xFE // little-endian BOM
	for i, u := range u16 {
		binary.LittleEndian.PutUint16(buf[2+i*2:], u)
	}
	if err := os.WriteFile(inPath, buf, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := run(inPath, outPath, "", ",", "utf16", true, false, true); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := "name,age\nAlice,30\n"
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
