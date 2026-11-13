package photo

import "testing"

func TestRejectBrokenFormulaWithoutGuessing(t *testing.T) {
	if ValidateQuestionText("Solve: x^2-5x+6\n6=0") == nil {
		t.Fatal("accepted corrupted formula")
	}
	if err := ValidateQuestionText("Solve: x^2-5x+6=0"); err != nil {
		t.Fatal(err)
	}
}
