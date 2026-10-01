package file

import (
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"
	"wayseer/pkg/sdk"
)

// defaultEventKind is the kind of events whose mapping names no type field.
const defaultEventKind = "event"

// eventMap makes an event of each record, about the entity of Kind in field ID when set.
type eventMap struct {
	Kind     sdk.Kind `yaml:"kind"`     // with id, the kind of the entity each event is about
	ID       string   `yaml:"id"`       // the field holding that entity's id; an empty cell makes a global event
	Time     string   `yaml:"time"`     // the field holding the time, RFC 3339 or Unix seconds; required
	Severity string   `yaml:"severity"` // the field holding debug, info, warn, error or critical; default info
	Type     string   `yaml:"type"`     // the field holding its type, such as deploy or alert; default event
	Message  string   `yaml:"message"`  // the field holding its message; required
	Fields   []string `yaml:"fields"`   // fields kept on the event
}

func (m *eventMap) validate() error {
	if m == nil {
		return nil
	}
	var errs []error
	if m.Time == "" {
		errs = append(errs, errors.New("events: empty time"))
	}
	if m.Message == "" {
		errs = append(errs, errors.New("events: empty message"))
	}
	switch {
	case m.ID == "" && m.Kind != "":
		errs = append(errs, errors.New("events: kind needs an id field"))
	case m.ID != "":
		errs = append(errs, validKindAndID("events", m.Kind, m.ID))
	}
	return errors.Join(errs...)
}

var severities = map[string]sdk.Severity{
	"": sdk.SevInfo, "debug": sdk.SevDebug, "info": sdk.SevInfo, "warn": sdk.SevWarn,
	"warning": sdk.SevWarn, "error": sdk.SevError, "crit": sdk.SevCritical, "critical": sdk.SevCritical,
}

// event maps one record; any bad field rejects it.
func (m *eventMap) event(inst sdk.ModuleID, r record) (sdk.Event, error) {
	e := sdk.Event{Source: inst, Kind: defaultEventKind}
	subject, err := m.subject(inst, r)
	if err != nil {
		return e, err
	}
	at, err := timestamp(r, m.Time)
	if err != nil {
		return e, err
	}
	sev, err := severity(r, m.Severity)
	if err != nil {
		return e, err
	}
	msg, _ := r.str(m.Message)
	if msg == "" {
		return e, fmt.Errorf("empty %s", m.Message)
	}
	if kind, _ := r.str(m.Type); kind != "" {
		e.Kind = kind
	}
	e.Entity, e.At, e.Severity, e.Message, e.Fields = subject, time.Unix(0, at).UTC(), sev, msg, attrs(r, m.Fields)
	e.ID = eventID(&e)
	return e, nil
}

// subject is the entity the event is about; empty when the mapping or the cell has no id.
func (m *eventMap) subject(inst sdk.ModuleID, r record) (sdk.EntityRef, error) {
	id, err := r.str(m.ID)
	if err != nil {
		return "", fmt.Errorf("%s: %w", m.ID, err)
	}
	if id == "" {
		return "", nil
	}
	return sdk.NewEntityRef(string(inst), m.Kind, id)
}

func severity(r record, field string) (sdk.Severity, error) {
	s, err := r.str(field)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", field, err)
	}
	sev, ok := severities[strings.ToLower(s)]
	if !ok {
		return 0, fmt.Errorf("%s %q (want debug, info, warn, error or critical)", field, s)
	}
	return sev, nil
}

// eventID hashes what identifies an event, so rereading a file gives the same ids.
func eventID(e *sdk.Event) string {
	h := fnv.New64a()
	for _, s := range []string{string(e.Entity), strconv.FormatInt(e.At.UnixNano(), 10), e.Kind, e.Message} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("event-%016x", h.Sum64())
}

func (s *source) mapEvent(inst sdk.ModuleID, r record, l *loaded) {
	if s.Events == nil {
		return
	}
	e, err := s.Events.event(inst, r)
	if err != nil {
		l.probs = append(l.probs, r.problem("%v", err))
		return
	}
	l.events = append(l.events, e)
}
