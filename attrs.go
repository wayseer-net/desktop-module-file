package file

import (
	"strconv"

	"wayseer.dev/sdk"
)

// attrSets lets a file's entities with the same attributes share one map; nothing changes an
// entity's attributes once it is made, and inventories repeat them often.
type attrSets struct {
	byKey   map[string]map[string]sdk.Value
	key     []byte
	text    []byte // one value's text, reused
	vals    []sdk.Value
	present []bool
}

// attrs reads fields from r, each number in its unit, sharing the map made for an earlier
// record with the same values.
func (a *attrSets) attrs(r record, fields []string, units map[string]sdk.Unit) map[string]sdk.Value {
	a.key, a.vals, a.present = a.key[:0], a.vals[:0], a.present[:0]
	shareable := true
	for _, f := range fields {
		v, ok := r.attr(f)
		v = v.In(units[f])
		a.vals, a.present = append(a.vals, v), append(a.present, ok)
		a.appendKey(v, ok)
		shareable = shareable && v.Type() != sdk.TypeList
	}
	if !shareable {
		return a.build(fields)
	}
	if m, ok := a.byKey[string(a.key)]; ok {
		return m
	}
	m := a.build(fields)
	if a.byKey == nil {
		a.byKey = map[string]map[string]sdk.Value{}
	}
	a.byKey[string(a.key)] = m
	return m
}

// build makes the map of the values last read, nil when none are present.
func (a *attrSets) build(fields []string) map[string]sdk.Value {
	var out map[string]sdk.Value
	for i, v := range a.vals {
		if !a.present[i] {
			continue
		}
		if out == nil {
			out = make(map[string]sdk.Value, len(fields))
		}
		out[fields[i]] = v
	}
	return out
}

// appendKey adds a value to the key with its type, unit and length, so "16" and 16 differ and no
// value's text can run into the next.
func (a *attrSets) appendKey(v sdk.Value, present bool) {
	if !present {
		a.key = append(a.key, 0)
		return
	}
	a.text = v.Append(a.text[:0])
	a.key = append(append(append(a.key, byte(v.Type())+1), v.Unit()...), ':')
	a.key = strconv.AppendInt(a.key, int64(len(a.text)), 10)
	a.key = append(append(a.key, ':'), a.text...)
}
