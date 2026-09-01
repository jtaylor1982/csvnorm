package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	if err := run(inPath, outPath, "", true, false, true); err != nil {
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

func TestRunStrictRejectsRaggedRows(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "ragged.csv")
	outPath := filepath.Join(dir, "clean.csv")

	if err := os.WriteFile(inPath, []byte("a,b,c\nd,e\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := run(inPath, outPath, "", true, true, true); err == nil {
		t.Error("expected error for ragged row under -strict, got nil")
	}
}
