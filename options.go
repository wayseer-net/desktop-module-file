package file

import (
	"errors"
	"fmt"
	"mindseye/pkg/sdk"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type options struct {
	Files  []source      `yaml:"files"`
	Rescan time.Duration `yaml:"rescan"` // how often to check files the watcher may have missed
	Replay bool          `yaml:"replay"` // move recorded times so the newest is when first loaded
}

// source is one file and how its records map to the world.
type source struct {
	Path      string     `yaml:"path"`
	Format    string     `yaml:"format"`    // csv or json; default from the extension
	Delimiter string     `yaml:"delimiter"` // CSV only; default ',' (tab for .tsv)
	Records   string     `yaml:"records"`   // JSON only: dotted path to the array of records
	Entities  *entityMap `yaml:"entities"`
	Series    *seriesMap `yaml:"series"`
	Events    *eventMap  `yaml:"events"`

	abs   string // cleaned absolute path, set by prepare
	delim rune
}

type entityMap struct {
	Kind   sdk.Kind  `yaml:"kind"`
	ID     string    `yaml:"id"`
	Name   string    `yaml:"name"`
	Status string    `yaml:"status"`
	Reason string    `yaml:"reason"`
	Attrs  []string  `yaml:"attrs"`
	Tags   string    `yaml:"tags"`
	Edges  []edgeMap `yaml:"edges"`
}

// edgeMap makes an edge from each record to the entities of Kind whose ids are in field To.
type edgeMap struct {
	Rel  sdk.Relation `yaml:"rel"`
	To   string       `yaml:"to"`
	Kind sdk.Kind     `yaml:"kind"`
}

type seriesMap struct {
	Kind    sdk.Kind             `yaml:"kind"`
	ID      string               `yaml:"id"`
	Time    string               `yaml:"time"`
	Metrics map[string]metricMap `yaml:"metrics"` // canonical metric name → its field
}

type metricMap struct {
	Field string   `yaml:"field"`
	Unit  sdk.Unit `yaml:"unit"`
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
