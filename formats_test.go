package file

import (
	"fmt"
	"mindseye/internal/model"
	"strings"
	"testing"
)

func TestReadJSONTopLevelArrayWithLines(t *testing.T) {
	src := "[\n  {\"id\": \"a\", \"n\": 1},\n  7,\n  {\"id\": \"b\"}\n]\n"
	recs, probs, err := readJSON([]byte(src), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].line != 2 || recs[1].line != 4 {
		t.Errorf("records = %+v", recs)
	}
	if len(probs) != 1 || probs[0].line != 3 {
		t.Errorf("problems = %+v; want the non-object on line 3", probs)
	}
	if v, _ := recs[0].get("n"); !v.Equal(model.Number(1)) {
		t.Errorf("n = %v", v)
	}
}

func TestReadJSONMissingRecordsPath(t *testing.T) {
	if _, _, err := readJSON([]byte(`{"data": {}}`), "data.items"); err == nil || !strings.Contains(err.Error(), "items") {
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
