// Command csvnorm reads a CSV file that may have inconsistent delimiters,
// ragged rows, stray whitespace, a non-UTF-8 encoding, or a leading
// byte-order mark, and writes out a clean, RFC 4180 CSV file with a comma
// delimiter and uniform column counts.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf16"
)

const bom = "﻿"

// candidateDelims are checked, in order, when the delimiter isn't given
// explicitly. Comma is listed first so it wins ties, since it's by far
// the most common case.
var candidateDelims = []rune{',', ';', '\t', '|'}

func main() {
	var (
		inPath    = flag.String("in", "", "input CSV file (default: stdin)")
		outPath   = flag.String("out", "", "output CSV file (default: stdout)")
		delim     = flag.String("delim", "", "input delimiter; auto-detected if omitted")
		outDelim  = flag.String("out-delim", ",", "output delimiter")
		encoding  = flag.String("encoding", "", "input encoding: utf8 (default), latin1, utf16, utf16le, or utf16be")
		trim      = flag.Bool("trim", true, "trim leading/trailing whitespace from each field")
		strict    = flag.Bool("strict", false, "fail on ragged rows instead of padding/truncating them")
		dropEmpty = flag.Bool("drop-empty", true, "drop rows where every field is empty")
		normDates = flag.Bool("normalize-dates", false, "rewrite columns of consistently-formatted dates as YYYY-MM-DD")
		normNums  = flag.Bool("normalize-numbers", false, "rewrite columns of consistently-formatted numbers, stripping currency symbols and thousands separators")
	)
	flag.Parse()

	if err := run(*inPath, *outPath, *delim, *outDelim, *encoding, *trim, *strict, *dropEmpty, *normDates, *normNums); err != nil {
		fmt.Fprintln(os.Stderr, "csvnorm:", err)
		os.Exit(1)
	}
}

func run(inPath, outPath, delimFlag, outDelimFlag, encoding string, trim, strict, dropEmpty, normDates, normNumbers bool) error {
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

	raw, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}

	decoded, err := decodeToUTF8(raw, encoding)
	if err != nil {
		return fmt.Errorf("decoding input: %w", err)
	}

	reader := bufio.NewReader(bytes.NewReader(decoded))
	stripLeadingBOM(reader)

	delim, err := resolveDelim(reader, delimFlag)
	if err != nil {
		return err
	}

	outDelim, err := parseDelimChar(outDelimFlag)
	if err != nil {
		return fmt.Errorf("output delimiter: %w", err)
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

	rows := make([][]string, 0, len(records))
	for _, rec := range records {
		rec = normalizeRow(rec, width, trim)
		if dropEmpty && rowIsEmpty(rec) {
			continue
		}
		rows = append(rows, rec)
	}

	normalizeColumns(rows, width, normDates, normNumbers)

	w := csv.NewWriter(out)
	w.Comma = outDelim
	for _, rec := range rows {
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
		return parseDelimChar(delimFlag)
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

// parseDelimChar validates that s is exactly one character and returns it
// as a rune, used for both -delim and -out-delim.
func parseDelimChar(s string) (rune, error) {
	runes := []rune(s)
	if len(runes) != 1 {
		return 0, fmt.Errorf("delimiter must be a single character, got %q", s)
	}
	return runes[0], nil
}

// decodeToUTF8 converts raw bytes in the named encoding to UTF-8. An empty
// encoding means the input is already UTF-8, which covers plain ASCII too.
func decodeToUTF8(data []byte, encoding string) ([]byte, error) {
	switch strings.ToLower(encoding) {
	case "", "utf8", "utf-8":
		return data, nil
	case "latin1", "iso-8859-1", "iso8859-1":
		return latin1ToUTF8(data), nil
	case "utf16", "utf-16":
		return decodeUTF16(data, binary.LittleEndian)
	case "utf16le", "utf-16le":
		return decodeUTF16(data, binary.LittleEndian)
	case "utf16be", "utf-16be":
		return decodeUTF16(data, binary.BigEndian)
	default:
		return nil, fmt.Errorf("unknown encoding %q (want utf8, latin1, utf16, utf16le, or utf16be)", encoding)
	}
}

// latin1ToUTF8 converts ISO-8859-1 bytes to UTF-8. Every Latin-1 byte maps
// directly onto the Unicode code point of the same value, so this can't fail.
func latin1ToUTF8(data []byte) []byte {
	runes := make([]rune, len(data))
	for i, b := range data {
		runes[i] = rune(b)
	}
	return []byte(string(runes))
}

// decodeUTF16 converts UTF-16 bytes to UTF-8. If data starts with a byte-
// order mark, that overrides the given default order. defaultOrder is used
// as-is otherwise, so -encoding utf16le/utf16be still respects a BOM if one
// happens to be present, and only falls back to the flag's order without one.
func decodeUTF16(data []byte, defaultOrder binary.ByteOrder) ([]byte, error) {
	order := defaultOrder
	switch {
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE:
		order = binary.LittleEndian
		data = data[2:]
	case len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF:
		order = binary.BigEndian
		data = data[2:]
	}
	if len(data)%2 != 0 {
		return nil, fmt.Errorf("utf16 input has odd byte length %d", len(data))
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = order.Uint16(data[i*2 : i*2+2])
	}
	return []byte(string(utf16.Decode(units))), nil
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

// dateLayouts are tried, in order, when detecting a date column. Only
// layouts with a four-digit or spelled-out year are included so month/day
// order never has to be guessed from an ambiguous two-digit field.
var dateLayouts = []string{
	"2006-01-02",
	"2006/01/02",
	"01/02/2006",
	"01-02-2006",
	"January 2, 2006",
	"Jan 2, 2006",
	"2 January 2006",
	"2 Jan 2006",
}

// normalizeColumns rewrites columns of dates and/or numbers to a single
// canonical format, one column at a time. rows[0] is treated as a header
// and left untouched. A column is only touched when every non-empty value
// below the header agrees on the same format, so a column that mixes
// formats, or isn't dates/numbers at all, is left exactly as it was rather
// than guessed at.
func normalizeColumns(rows [][]string, width int, normDates, normNumbers bool) {
	if !normDates && !normNumbers {
		return
	}
	if len(rows) < 2 {
		return
	}
	data := rows[1:]

	for col := 0; col < width; col++ {
		values := make([]string, len(data))
		for i, row := range data {
			values[i] = row[col]
		}

		if normNumbers && columnLooksNumeric(values) {
			for _, row := range data {
				if row[col] == "" {
					continue
				}
				if n, ok := parseNumber(row[col]); ok {
					row[col] = n
				}
			}
			continue
		}

		if normDates {
			if layout, ok := columnDateLayout(values); ok {
				for _, row := range data {
					if row[col] == "" {
						continue
					}
					if t, err := time.Parse(layout, row[col]); err == nil {
						row[col] = t.Format("2006-01-02")
					}
				}
			}
		}
	}
}

// columnDateLayout returns the first layout in dateLayouts that parses
// every non-empty value in values, if any layout does.
func columnDateLayout(values []string) (string, bool) {
	for _, layout := range dateLayouts {
		matched := false
		for _, v := range values {
			if v == "" {
				continue
			}
			if _, err := time.Parse(layout, v); err != nil {
				matched = false
				break
			}
			matched = true
		}
		if matched {
			return layout, true
		}
	}
	return "", false
}

// columnLooksNumeric reports whether every non-empty value in values
// parses as a number, and at least one of them does.
func columnLooksNumeric(values []string) bool {
	seen := false
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := parseNumber(v); !ok {
			return false
		}
		seen = true
	}
	return seen
}

// parseNumber normalizes s to a plain decimal number: an optional leading
// sign, digits, and an optional "." decimal point, with a leading currency
// symbol and thousands separators stripped. It accepts both "1,234.56" and
// "1.234,56" style grouping, deciding which of "," and "." is the decimal
// separator from whichever one appears last in the string. When only one
// of the two appears, a trailing group of exactly three digits is treated
// as thousands grouping rather than a decimal fraction, since that's the
// far more common case in real data ("1,234" almost always means one
// thousand two hundred thirty-four, not 1.234).
func parseNumber(s string) (string, bool) {
	for _, sym := range []string{"$", "€", "£", "¥"} {
		if strings.HasPrefix(s, sym) {
			s = strings.TrimPrefix(s, sym)
			break
		}
	}
	sign := ""
	if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		sign = s[:1]
		s = s[1:]
	}
	if s == "" {
		return "", false
	}

	lastComma := strings.LastIndexByte(s, ',')
	lastDot := strings.LastIndexByte(s, '.')

	var intPart, fracPart string
	switch {
	case lastComma == -1 && lastDot == -1:
		intPart = s
	case lastComma != -1 && lastDot != -1:
		if lastComma > lastDot {
			intPart = strings.ReplaceAll(s[:lastComma], ".", "")
			fracPart = s[lastComma+1:]
		} else {
			intPart = strings.ReplaceAll(s[:lastDot], ",", "")
			fracPart = s[lastDot+1:]
		}
	case lastComma != -1:
		if isThousandsGrouping(s, ',') {
			intPart = strings.ReplaceAll(s, ",", "")
		} else if strings.Count(s, ",") == 1 {
			parts := strings.SplitN(s, ",", 2)
			intPart, fracPart = parts[0], parts[1]
		} else {
			return "", false
		}
	default:
		if isThousandsGrouping(s, '.') {
			intPart = strings.ReplaceAll(s, ".", "")
		} else if strings.Count(s, ".") == 1 {
			parts := strings.SplitN(s, ".", 2)
			intPart, fracPart = parts[0], parts[1]
		} else {
			return "", false
		}
	}

	if !isDigits(intPart) {
		return "", false
	}
	if fracPart == "" {
		return sign + intPart, true
	}
	if !isDigits(fracPart) {
		return "", false
	}
	return sign + intPart + "." + fracPart, true
}

// isThousandsGrouping reports whether s is digit groups joined by sep in
// the standard thousands pattern: a leading group of one to three digits
// followed by one or more groups of exactly three digits, e.g. "1,234" or
// "12,345,678".
func isThousandsGrouping(s string, sep byte) bool {
	groups := strings.Split(s, string(sep))
	if len(groups) < 2 {
		return false
	}
	if len(groups[0]) == 0 || len(groups[0]) > 3 || !isDigits(groups[0]) {
		return false
	}
	for _, g := range groups[1:] {
		if len(g) != 3 || !isDigits(g) {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
