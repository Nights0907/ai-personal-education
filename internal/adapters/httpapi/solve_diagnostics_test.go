package httpapi

import "testing"

func TestIncompleteSubmissionDiagnostics(t *testing.T) {
	for _, c := range []struct {
		d    solveDiagnostics
		code string
	}{
		{solveDiagnostics{}, "MODEL_STOPPED_BEFORE_VALIDATION"},
		{solveDiagnostics{Validated: true}, "SOLUTION_VALIDATION_FAILED"},
		{solveDiagnostics{Validated: true, Passed: true}, "MODEL_STOPPED_BEFORE_COMMIT"},
		{solveDiagnostics{Validated: true, Passed: true, Committing: true}, "SOLUTION_COMMIT_FAILED"},
	} {
		if got, _ := c.d.failure(); got != c.code {
			t.Fatalf("got %s want %s", got, c.code)
		}
	}
}
