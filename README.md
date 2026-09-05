# csvnorm

CSV files that come out of spreadsheets, exports, or other people's scripts
are rarely clean. The delimiter might be a semicolon because someone's
locale uses commas for decimals, rows might have three fields on one line
and five on the next, there's a stray byte-order mark at the top, and half
the fields have leading or trailing whitespace. csvnorm takes that kind of
file and turns it into a plain, consistent CSV: comma-delimited, every row
the same width, no BOM, no surprise whitespace.

It's a single small command-line tool, no configuration file, no server.

## Usage

Read from stdin, write to stdout:

```sh
csvnorm < messy.csv > clean.csv
```

Or name files explicitly:

```sh
csvnorm -in messy.csv -out clean.csv
```

### What it does

- Auto-detects the delimiter (comma, semicolon, tab, or pipe) by counting
  occurrences on the first line, unless you pass `-delim`.
- Writes comma-delimited output by default, independent of whatever the
  input used; pass `-out-delim` to write something else instead (a tab, a
  pipe, whatever the downstream tool expects).
- Strips a leading UTF-8 byte-order mark if present.
- Trims leading/trailing whitespace from every field (disable with `-trim=false`).
- Pads short rows and truncates long rows to match the header's column
  count, so every output row is the same width. Pass `-strict` to fail
  instead of silently fixing ragged rows.
- Drops rows where every field is empty (disable with `-drop-empty=false`).
- Recovers from a quoted field that's never closed (a common broken-exporter
  bug that would otherwise swallow every row after it into one field, or
  fail outright). When that happens, csvnorm falls back to treating each
  physical line as one row, prints a warning to stderr, and keeps going.

### Example

Input (`messy.csv`), semicolon-delimited with a ragged row and extra
whitespace:

```
name; age ;city
Alice ;30; Springfield
Bob;; Shelbyville
 ; ; 
```

```sh
csvnorm -in messy.csv
```

Output:

```
name,age,city
Alice,30,Springfield
Bob,,Shelbyville
```

## Flags

| Flag           | Default | Meaning                                            |
|----------------|---------|-----------------------------------------------------|
| `-in`          | stdin   | input file path                                      |
| `-out`         | stdout  | output file path                                     |
| `-delim`       | auto    | input delimiter, single character                   |
| `-out-delim`   | `,`     | output delimiter, single character                   |
| `-trim`        | true    | trim whitespace from each field                      |
| `-strict`      | false   | error on ragged rows instead of padding/truncating   |
| `-drop-empty`  | true    | drop rows where every field is empty                 |

## Building

Standard library only, no dependencies to fetch:

```sh
go build -o csvnorm .
```

## Status

Early. Handles the common messiness (delimiter guessing, ragged rows, BOM,
whitespace, unterminated quotes, choosing an output delimiter) but not yet
things like mixed encodings or column-level type normalization. See the
issue tracker for what's next.
