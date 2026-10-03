package file

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"wayseer.dev/sdk"
)

// record is one CSV row or JSON object, starting at line.
type record struct {
	line int
	text bool // values are CSV cells, so attributes infer numbers and booleans
	get  func(field string) (sdk.Value, bool)
}

// problem is a malformed record, or a whole file that could not be used when line is 0.
type problem struct {
	line int
	msg  string
}

func (r record) problem(format string, args ...any) problem {
	return problem{line: r.line, msg: fmt.Sprintf(format, args...)}
}

// str reads field as text; numbers and booleans are formatted.
func (r record) str(field string) (string, error) {
	v, ok := r.get(field)
	if !ok {
		return "", nil
	}
	return text(v)
}

// attr reads field as an attribute value, inferring the type of CSV text.
func (r record) attr(field string) (sdk.Value, bool) {
	v, ok := r.get(field)
	if ok && r.text {
		return infer(v.Str()), true
	}
	return v, ok
}

// list reads a field holding several texts: a JSON array, or a CSV cell split on ';'.
func (r record) list(field string) ([]string, error) {
	v, ok := r.get(field)
	switch {
	case !ok:
		return nil, nil
	case r.text:
		return splitCell(v.Str()), nil
	case v.Type() != sdk.TypeList:
		s, err := text(v)
		return []string{s}, err
	}
	out := make([]string, 0, len(v.List()))
	for _, item := range v.List() {
		s, err := text(item)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// number reads field as a finite number; ok is false when the field is absent.
func (r record) number(field string) (f float64, ok bool, err error) {
	v, ok := r.get(field)
	switch {
	case !ok:
		return 0, false, nil
	case v.Type() == sdk.TypeNumber:
		f = v.Num()
	case v.Type() == sdk.TypeString:
		if f, err = strconv.ParseFloat(strings.TrimSpace(v.Str()), 64); err != nil {
			return 0, true, fmt.Errorf("%s: %q is not a number", field, v.Str())
		}
	default:
		return 0, true, fmt.Errorf("%s: %v is not a number", field, v)
	}
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, true, fmt.Errorf("%s: %v is not finite", field, f)
	}
	return f, true, nil
}

var errNotText = errors.New("a list is not a single value")

func text(v sdk.Value) (string, error) {
	switch v.Type() {
	case sdk.TypeString:
		return v.Str(), nil
	case sdk.TypeNumber:
		return strconv.FormatFloat(v.Num(), 'f', -1, 64), nil
	case sdk.TypeBool:
		return strconv.FormatBool(v.Bool()), nil
	}
	return "", errNotText
}

func infer(s string) sdk.Value {
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
		return sdk.Number(f)
	}
	switch strings.ToLower(s) {
	case "true":
		return sdk.Bool(true)
	case "false":
		return sdk.Bool(false)
	}
	return sdk.String(s)
}

func splitCell(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ";") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// jsonValue converts decoded JSON; nested objects become their compact JSON text.
func jsonValue(v any) (sdk.Value, bool) {
	switch x := v.(type) {
	case string:
		return sdk.String(x), true
	case float64:
		return sdk.Number(x), true
	case bool:
		return sdk.Bool(x), true
	case []any:
		out := make([]sdk.Value, 0, len(x))
		for _, item := range x {
			if iv, ok := jsonValue(item); ok {
				out = append(out, iv)
			}
		}
		return sdk.List(out...), true
	case map[string]any:
		b, _ := json.Marshal(x) // cannot fail: it was decoded from JSON
		return sdk.String(string(b)), true
	}
	return sdk.Value{}, false
}
