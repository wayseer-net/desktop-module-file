package file

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mindseye/internal/model"
	"strings"
)

var bom = []byte("\xef\xbb\xbf")

// readCSV reads a header row, then one record per row; malformed rows become problems.
func readCSV(src []byte, delim rune) (cols []string, recs []record, probs []problem, err error) {
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(src, bom)))
	r.Comma, r.Comment, r.TrimLeadingSpace = delim, '#', true
	if cols, err = r.Read(); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil, nil, errors.New("no header row")
		}
		return nil, nil, nil, fmt.Errorf("header: %w", err)
	}
	index := make(map[string]int, len(cols))
	for i, c := range cols {
		cols[i] = strings.TrimSpace(c)
		if _, dup := index[cols[i]]; dup {
			return nil, nil, nil, fmt.Errorf("column %q appears twice", cols[i])
		}
		index[cols[i]] = i
	}
	for {
		row, err := r.Read()
		var pe *csv.ParseError
		switch {
		case errors.Is(err, io.EOF):
			return cols, recs, probs, nil
		case errors.As(err, &pe):
			probs = append(probs, problem{line: pe.StartLine, msg: pe.Err.Error()})
		case err != nil:
			return nil, nil, nil, err
		default:
			line, _ := r.FieldPos(0)
			recs = append(recs, csvRecord(index, row, line))
		}
	}
}

func csvRecord(index map[string]int, row []string, line int) record {
	return record{line: line, text: true, get: func(field string) (model.Value, bool) {
		i, ok := index[field]
		if !ok {
			return model.Value{}, false
		}
		cell := strings.TrimSpace(row[i])
		return model.String(cell), cell != ""
	}}
}

// readJSON reads the array of objects at the dotted path, noting each object's line.
func readJSON(src []byte, path string) ([]record, []problem, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	if err := descend(dec, path); err != nil {
		return nil, nil, err
	}
	if err := expectDelim(dec, '[', "an array of records"); err != nil {
		return nil, nil, err
	}
	var recs []record
	var probs []problem
	for dec.More() {
		line := lineAt(src, dec.InputOffset())
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", line, err)
		}
		obj, ok := v.(map[string]any)
		if !ok {
			probs = append(probs, problem{line: line, msg: "record is not an object"})
			continue
		}
		recs = append(recs, jsonRecord(obj, line))
	}
	return recs, probs, nil
}

// descend moves dec to the value at the dotted path of object keys.
func descend(dec *json.Decoder, path string) error {
	if path == "" {
		return nil
	}
	for key := range strings.SplitSeq(path, ".") {
		if err := expectDelim(dec, '{', "an object holding "+key); err != nil {
			return err
		}
		if err := findKey(dec, key); err != nil {
			return err
		}
	}
	return nil
}

// findKey skips an object's members up to the value of key.
func findKey(dec *json.Decoder, key string) error {
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if tok == key {
			return nil
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err
		}
	}
	return fmt.Errorf("records: no field %q", key)
}

func expectDelim(dec *json.Decoder, want json.Delim, what string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != want {
		return fmt.Errorf("want %s, found %v", what, tok)
	}
	return nil
}

// lineAt is the line of the first byte at or after off that starts a value.
func lineAt(src []byte, off int64) int {
	for int(off) < len(src) && strings.IndexByte(" \t\r\n,", src[off]) >= 0 {
		off++
	}
	return 1 + bytes.Count(src[:min(int(off), len(src))], []byte("\n"))
}

func jsonRecord(obj map[string]any, line int) record {
	return record{line: line, get: func(field string) (model.Value, bool) { return jsonValue(lookup(obj, field)) }}
}

// lookup finds field as a key, or else by following its dots through nested objects.
func lookup(obj map[string]any, field string) any {
	if v, ok := obj[field]; ok {
		return v
	}
	head, rest, ok := strings.Cut(field, ".")
	if !ok {
		return nil
	}
	if inner, ok := obj[head].(map[string]any); ok {
		return lookup(inner, rest)
	}
	return nil
}
