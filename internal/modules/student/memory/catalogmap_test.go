package memory

import "testing"

func TestMapSolveKnowledge(t *testing.T) {
	cases := map[string]string{
		"sets_logic":             "ALG",
		"equations_inequalities": "ALG",
		"functions_derivatives":  "FUNC",
		"sequences":              "SEQ",
		"trigonometry":           "TRIG",
		"analytic_geometry":      "ANALYTIC",
		"solid_geometry":         "SOLID",
		"probability_statistics": "PROB",
	}
	for in, want := range cases {
		if got := MapSolveKnowledge(in); got != want {
			t.Fatalf("MapSolveKnowledge(%q) = %q, want %q", in, got, want)
		}
	}
	if got := MapSolveKnowledge("not_a_real_code"); got != "" {
		t.Fatalf("unknown code must map to empty, got %q", got)
	}
}
