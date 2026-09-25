package file

import (
	"errors"
	"fmt"
	"hash/fnv"
	"mindseye/internal/model"
	"strconv"
	"strings"
	"time"
)

// defaultEventKind is the kind of events whose mapping names no type field.
const defaultEventKind = "event"

// eventMap makes an event of each record, about the entity of Kind in field ID when set.
type eventMap struct {
	Kind     model.Kind `yaml:"kind"`
	ID       string     `yaml:"id"` // an empty cell makes a global event
	Time     string     `yaml:"time"`
	Severity string     `yaml:"severity"` // default info
	Type     string     `yaml:"type"`     // e.g. deploy or alert; default "event"
	Message  string     `yaml:"message"`
	Fields   []string   `yaml:"fields"`
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

var severities = map[string]model.Severity{
	"": model.SevInfo, "debug": model.SevDebug, "info": model.SevInfo, "warn": model.SevWarn,
	"warning": model.SevWarn, "error": model.SevError, "crit": model.SevCritical, "critical": model.SevCritical,
}

// event maps one record; any bad field rejects it.
func (m *eventMap) event(inst model.ModuleID, r record) (model.Event, error) {
	e := model.Event{Source: inst, Kind: defaultEventKind}
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
func (m *eventMap) subject(inst model.ModuleID, r record) (model.EntityRef, error) {
	id, err := r.str(m.ID)
	if err != nil {
		return "", fmt.Errorf("%s: %w", m.ID, err)
	}
	if id == "" {
		return "", nil
	}
	return model.NewEntityRef(string(inst), m.Kind, id)
}

func severity(r record, field string) (model.Severity, error) {
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
func eventID(e *model.Event) string {
	h := fnv.New64a()
	for _, s := range []string{string(e.Entity), strconv.FormatInt(e.At.UnixNano(), 10), e.Kind, e.Message} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("event-%016x", h.Sum64())
}

func (s *source) mapEvent(inst model.ModuleID, r record, l *loaded) {
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
