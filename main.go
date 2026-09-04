// Command csvnorm reads a CSV file that may have inconsistent delimiters,
// ragged rows, stray whitespace, or a leading byte-order mark, and writes
// out a clean, RFC 4180 CSV file with a comma delimiter and uniform column
// counts.
package main

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const bom = "﻿"

// candidateDelims are checked, in order, when the delimiter isn't given
// explicitly. Comma is listed first so it wins ties, since it's by far
// the most common case.
var candidateDelims = []rune{',', ';', '\t', '|'}

func main() {
	var (
		inPath  = flag.String("in", "", "input CSV file (default: stdin)")
		outPath = flag.String("out", "", "output CSV file (default: stdout)")
		delim   = flag.String("delim", "", "input delimiter; auto-detected if omitted")
		trim    = flag.Bool("trim", true, "trim leading/trailing whitespace from each field")
		strict  = flag.Bool("strict", false, "fail on ragged rows instead of padding/truncating them")
		dropEmpty = flag.Bool("drop-empty", true, "drop rows where every field is empty")
	)
	flag.Parse()

	if err := run(*inPath, *outPath, *delim, *trim, *strict, *dropEmpty); err != nil {
		fmt.Fprintln(os.Stderr, "csvnorm:", err)
		os.Exit(1)
	}
}

func run(inPath, outPath, delimFlag string, trim, strict, dropEmpty bool) error {
	in, err := openInput(inPath)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := openOutput(outPath)
	if err != nil {
		return err
	}
	defer out.Close()

	reader := bufio.NewReader(in)
	stripLeadingBOM(reader)

	delim, err := resolveDelim(reader, delimFlag)
	if err != nil {
		return err
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}

	records, fellBack, err := parseRecords(data, delim)
	if err != nil {
		return fmt.Errorf("reading CSV: %w", err)
	}
	if fellBack {
		fmt.Fprintln(os.Stderr, "csvnorm: malformed quoting detected, falling back to line-based parsing")
	}
	if len(records) == 0 {
		return nil
	}

	width := len(records[0])
	if strict {
		for i, rec := range records {
			if len(rec) != width {
				return fmt.Errorf("row %d has %d fields, want %d (use -strict=false to pad/truncate)", i+1, len(rec), width)
			}
		}
	}

	w := csv.NewWriter(out)
	for _, rec := range records {
		rec = normalizeRow(rec, width, trim)
		if dropEmpty && rowIsEmpty(rec) {
			continue
		}
		if err := w.Write(rec); err != nil {
			return fmt.Errorf("writing CSV: %w", err)
		}
	}
	w.Flush()
	return w.Error()
}

func openInput(path string) (io.ReadCloser, error) {
	if path == "" {
		return io.NopCloser(os.Stdin), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening input: %w", err)
	}
	return f, nil
}

func openOutput(path string) (io.WriteCloser, error) {
	if path == "" {
		return nopWriteCloser{os.Stdout}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("creating output: %w", err)
	}
	return f, nil
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// stripLeadingBOM consumes a UTF-8 byte-order mark from r if present,
// leaving the stream positioned right after it.
func stripLeadingBOM(r *bufio.Reader) {
	peek, err := r.Peek(len(bom))
	if err == nil && string(peek) == bom {
		r.Discard(len(bom))
	}
}

// resolveDelim returns delimFlag as a rune if set, otherwise sniffs the
// first line of r (without consuming it) and picks whichever candidate
// delimiter appears most often.
func resolveDelim(r *bufio.Reader, delimFlag string) (rune, error) {
	if delimFlag != "" {
		runes := []rune(delimFlag)
		if len(runes) != 1 {
			return 0, fmt.Errorf("delimiter must be a single character, got %q", delimFlag)
		}
		return runes[0], nil
	}

	peek, _ := r.Peek(4096)
	line := string(peek)
	if i := strings.IndexAny(line, "\n\r"); i >= 0 {
		line = line[:i]
	}

	best := candidateDelims[0]
	bestCount := -1
	for _, d := range candidateDelims {
		count := strings.Count(line, string(d))
		if count > bestCount {
			best = d
			bestCount = count
		}
	}
	return best, nil
}

// parseRecords parses data as delim-separated CSV. If a quoted field is
// left open (a broken exporter's stray or missing closing quote), the
// standard reader either swallows the rest of the file into one field or
// fails outright with csv.ErrQuote. In that case we fall back to a
// permissive, line-based split: quotes are only honored within a single
// physical line, so a malformed quote can no longer eat the rows that
// follow it. The fellBack return value reports whether that happened.
func parseRecords(data []byte, delim rune) (records [][]string, fellBack bool, err error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = delim
	r.FieldsPerRecord = -1 // rows may be ragged; we normalize them ourselves
	r.LazyQuotes = true
	r.TrimLeadingSpace = true

	records, err = r.ReadAll()
	if err == nil {
		return records, false, nil
	}

	var parseErr *csv.ParseError
	if !errors.As(err, &parseErr) || parseErr.Err != csv.ErrQuote {
		return nil, false, err
	}
	return splitPermissive(data, delim), true, nil
}

// splitPermissive treats each physical line of data as one record, split
// on delim. Unlike the RFC 4180 reader it never lets a field span
// multiple lines, so a stray or missing quote only affects the line it's
// on rather than swallowing every row after it.
func splitPermissive(data []byte, delim rune) [][]string {
	var records [][]string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			// blank lines are skipped, matching encoding/csv's behavior
			continue
		}
		records = append(records, splitLinePermissive(line, delim))
	}
	return records
}

// splitLinePermissive splits a single line on delim, honoring double
// quotes as a way to embed delim in a field (with "" as an escaped
// literal quote). A quote left open at end of line simply makes the rest
// of the line part of that field instead of erroring.
func splitLinePermissive(line string, delim rune) []string {
	var fields []string
	var buf strings.Builder
	inQuotes := false
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '"':
			if inQuotes && i+1 < len(runes) && runes[i+1] == '"' {
				buf.WriteRune('"')
				i++
			} else {
				inQuotes = !inQuotes
			}
		case c == delim && !inQuotes:
			fields = append(fields, buf.String())
			buf.Reset()
		default:
			buf.WriteRune(c)
		}
	}
	fields = append(fields, buf.String())
	return fields
}

// normalizeRow trims fields (if requested) and pads or truncates the row
// to exactly width columns.
func normalizeRow(rec []string, width int, trim bool) []string {
	if trim {
		for i, f := range rec {
			rec[i] = strings.TrimSpace(f)
		}
	}
	switch {
	case len(rec) < width:
		padded := make([]string, width)
		copy(padded, rec)
		return padded
	case len(rec) > width:
		return rec[:width]
	default:
		return rec
	}
}

func rowIsEmpty(rec []string) bool {
	for _, f := range rec {
		if f != "" {
			return false
		}
	}
	return true
}
