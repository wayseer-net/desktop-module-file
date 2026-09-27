package file

import (
	"fmt"
	"mindseye/pkg/sdk"
	"strings"
	"testing"
)

func TestReadJSONTopLevelArrayWithLines(t *testing.T) {
	src := "[\n  {\"id\": \"a\", \"n\": 1},\n  7,\n  {\"id\": \"b\"}\n]\n"
	var recs []record
	probs, err := readJSON([]byte(src), "", func(r record) { recs = append(recs, r) })
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].line != 2 || recs[1].line != 4 {
		t.Errorf("records = %+v", recs)
	}
	if len(probs) != 1 || probs[0].line != 3 {
		t.Errorf("problems = %+v; want the non-object on line 3", probs)
	}
	if v, _ := recs[0].get("n"); !v.Equal(sdk.Number(1)) {
		t.Errorf("n = %v", v)
	}
}

func TestReadJSONMissingRecordsPath(t *testing.T) {
	if _, err := readJSON([]byte(`{"data": {}}`), "data.items", func(record) {}); err == nil || !strings.Contains(err.Error(), "items") {
		t.Errorf("err = %v; want the missing field named", err)
	}
}

func TestProblemsAreCappedPerFile(t *testing.T) {
	var probs []problem
	for i := range problemCap + 5 {
		probs = append(probs, problem{line: i + 2, msg: "bad"})
	}
	got := reports("x.csv", probs)
	if len(got) != problemCap+1 || got[problemCap].msg != fmt.Sprintf("x.csv: %d more problems not shown", 5) {
		t.Errorf("last of %d reports = %+v", len(got), got[len(got)-1])
	}
}

func TestReadCSVSkipsAByteOrderMark(t *testing.T) {
	var ids []string
	src := strings.NewReader("\xef\xbb\xbfid,n\na,1\nb,2\n")
	probs, err := readCSV(src, ',', func(cols []string) error {
		if cols[0] != "id" {
			return fmt.Errorf("first column %q", cols[0])
		}
		return nil
	}, func(r record) { id, _ := r.str("id"); ids = append(ids, id) })
	if err != nil || len(probs) != 0 || strings.Join(ids, " ") != "a b" {
		t.Errorf("ids %q, problems %v, err %v", ids, probs, err)
	}
}
