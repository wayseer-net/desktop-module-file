package file

import (
	"errors"
	"fmt"
	"mindseye/pkg/sdk"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type options struct {
	Files  []source      `yaml:"files"`  // the files read, and how their records map to the world; required
	Rescan time.Duration `yaml:"rescan"` // how often to check files the watcher may have missed; default 5s
	Replay bool          `yaml:"replay"` // move recorded times so the newest is when the files were first loaded; default false
}

// source is one file and how its records map to the world.
type source struct {
	Path      string     `yaml:"path"`      // the file; ~/ is the home directory; required
	Format    string     `yaml:"format"`    // the format: csv, tsv or json; default from the extension
	Delimiter string     `yaml:"delimiter"` // CSV only: the field separator; default a comma, or a tab for tsv
	Records   string     `yaml:"records"`   // JSON only: dotted path to the array of records; default the top-level array
	Entities  *entityMap `yaml:"entities"`  // each record as an entity
	Series    *seriesMap `yaml:"series"`    // each record as samples of metrics
	Events    *eventMap  `yaml:"events"`    // each record as an event

	abs   string // cleaned absolute path, set by prepare
	delim rune
}

type entityMap struct {
	Kind   sdk.Kind            `yaml:"kind"`   // the entities' kind, such as host or service; required
	ID     string              `yaml:"id"`     // the field holding each entity's id; required
	Name   string              `yaml:"name"`   // the field holding its name; default the id
	Status string              `yaml:"status"` // the field holding ok, warn, crit, down or unknown
	Reason string              `yaml:"reason"` // the field explaining the status
	Attrs  []string            `yaml:"attrs"`  // fields kept as attributes
	Units  map[string]sdk.Unit `yaml:"units"`  // the unit of a numeric attribute, by its field: bytes, seconds, percent and so on
	Tags   string              `yaml:"tags"`   // the field holding its tags
	Edges  []edgeMap           `yaml:"edges"`  // links from each entity to others
}

// edgeMap makes an edge from each record to the entities of Kind whose ids are in field To.
type edgeMap struct {
	Rel  sdk.Relation `yaml:"rel"`  // the relation, such as depends_on or parent_of; required
	To   string       `yaml:"to"`   // the field holding the ids it links to; required
	Kind sdk.Kind     `yaml:"kind"` // the kind of the entities it links to; required
}

type seriesMap struct {
	Kind    sdk.Kind             `yaml:"kind"`    // the kind of the entity each record is about; required
	ID      string               `yaml:"id"`      // the field holding that entity's id; required
	Time    string               `yaml:"time"`    // the field holding the time, RFC 3339 or Unix seconds; required
	Metrics map[string]metricMap `yaml:"metrics"` // metric name to its field and unit; required
}

type metricMap struct {
	Field string   `yaml:"field"` // the field holding the value; required
	Unit  sdk.Unit `yaml:"unit"`  // the unit: bytes, bytes_per_second, bits, bits_per_second, percent, ratio, seconds, count or per_second
}

const minRescan = 100 * time.Millisecond

// prepare checks the options and resolves each source's path, format and delimiter.
func (o *options) prepare() error {
	if len(o.Files) == 0 {
		return errors.New("no files configured")
	}
	if o.Rescan < minRescan {
		return fmt.Errorf("rescan %v is below %v", o.Rescan, minRescan)
	}
	seen := map[string]bool{}
	units := map[string]sdk.Unit{}
	for i := range o.Files {
		s := &o.Files[i]
		if err := s.prepare(units); err != nil {
			return fmt.Errorf("files[%d] (%s): %w", i, s.Path, err)
		}
		if seen[s.abs] {
			return fmt.Errorf("files[%d]: %s is listed twice", i, s.Path)
		}
		seen[s.abs] = true
	}
	return nil
}

func (s *source) prepare(units map[string]sdk.Unit) error {
	if s.Path == "" {
		return errors.New("empty path")
	}
	abs, err := filepath.Abs(expandHome(s.Path))
	if err != nil {
		return err
	}
	s.abs = abs
	if err := s.prepareFormat(); err != nil {
		return err
	}
	if s.Entities == nil && s.Series == nil && s.Events == nil {
		return errors.New("maps no entities, series or events")
	}
	return errors.Join(s.Entities.validate(), s.Series.validate(units), s.Events.validate())
}

func (s *source) prepareFormat() error {
	if s.Format == "" {
		switch ext := strings.ToLower(filepath.Ext(s.abs)); ext {
		case ".csv", ".tsv", ".json":
			s.Format = strings.TrimPrefix(ext, ".")
		default:
			return fmt.Errorf("cannot tell the format from %q; set format: csv or json", ext)
		}
	}
	switch s.Format {
	case "tsv", "csv":
		if s.Records != "" {
			return errors.New("records applies only to JSON")
		}
		return s.prepareDelimiter()
	case "json":
		if s.Delimiter != "" {
			return errors.New("delimiter applies only to CSV")
		}
		return nil
	}
	return fmt.Errorf("format %q (want csv or json)", s.Format)
}

func (s *source) prepareDelimiter() error {
	d := s.Delimiter
	switch {
	case d == "" && s.Format == "tsv":
		d = "\t"
	case d == "":
		d = ","
	}
	r, n := utf8.DecodeRuneInString(d)
	if n != len(d) || r == '"' || r == '\n' || r == '\r' || r == utf8.RuneError {
		return fmt.Errorf("delimiter %q must be one character other than a quote or newline", d)
	}
	s.Format, s.delim = "csv", r
	return nil
}

func (e *entityMap) validate() error {
	if e == nil {
		return nil
	}
	errs := []error{validKindAndID("entities", e.Kind, e.ID)}
	for i, ed := range e.Edges {
		if ed.To == "" {
			errs = append(errs, fmt.Errorf("entities.edges[%d]: empty to", i))
		}
		errs = append(errs, ed.Rel.Validate(), ed.Kind.Validate())
	}
	for field, u := range e.Units {
		if !slices.Contains(e.Attrs, field) {
			errs = append(errs, fmt.Errorf("entities.units: %s is not one of the attrs", field))
		}
		errs = append(errs, u.Validate())
	}
	return errors.Join(errs...)
}

func (m *seriesMap) validate(units map[string]sdk.Unit) error {
	if m == nil {
		return nil
	}
	errs := []error{validKindAndID("series", m.Kind, m.ID)}
	if m.Time == "" {
		errs = append(errs, errors.New("series: empty time"))
	}
	if len(m.Metrics) == 0 {
		errs = append(errs, errors.New("series: no metrics"))
	}
	for name, mm := range m.Metrics {
		if u, ok := units[name]; ok && u != mm.Unit {
			errs = append(errs, fmt.Errorf("series: metric %s has units %q and %q", name, u, mm.Unit))
		}
		units[name] = mm.Unit
		if name == "" || mm.Field == "" {
			errs = append(errs, fmt.Errorf("series: metric %q needs a name and a field", name))
		}
		if err := mm.Unit.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("series: metric %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func validKindAndID(what string, k sdk.Kind, id string) error {
	if id == "" {
		return fmt.Errorf("%s: empty id", what)
	}
	if err := k.Validate(); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// expandHome replaces a leading ~/ with the home directory.
func expandHome(p string) string {
	rest, ok := strings.CutPrefix(p, "~/")
	if !ok {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, rest)
	}
	return p
}
