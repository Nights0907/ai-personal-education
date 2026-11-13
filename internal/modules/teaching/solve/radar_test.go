package solve

import (
	"encoding/json"
	"testing"
)

func TestRadarVariantsAreStrictlyVerified(t *testing.T) {
	for _, stem := range []string{"解方程4x-7=13", "等差数列首项为4，公差为3，求第8项和前8项和。", "求函数 f(x)=x²-4x+3 在区间[0,3]上的最大值和最小值。"} {
		source := View{Question: Question{Stem: stem, Difficulty: "easy"}}
		for seed := 1; seed <= 200; seed++ {
			candidate, err := RadarVariant(source, seed)
			if err != nil {
				t.Fatalf("%s %d: %v", stem, seed, err)
			}
			if err = CheckRadarCandidate(source, seed, candidate); err != nil {
				t.Fatal(err)
			}
			if candidate.Question.Stem == stem {
				t.Fatal("same original question")
			}
			encoded, _ := json.Marshal(candidate)
			var changed Candidate
			json.Unmarshal(encoded, &changed)
			changed.Solution.Answer[0].Content = "错误答案"
			if CheckRadarCandidate(source, seed, changed) == nil {
				t.Fatal("wrong answer accepted")
			}
			json.Unmarshal(encoded, &changed)
			changed.Question.Stem += "且x=999"
			if CheckRadarCandidate(source, seed, changed) == nil {
				t.Fatal("contradictory conditions accepted")
			}
		}
	}
}
func TestRadarRefusesUnsupportedOrInvalidInputs(t *testing.T) {
	for _, stem := range []string{"证明三角形全等", "求函数f(x)=x²-4x+3+sin(x)在[0,3]上的最大值", "求函数f(x)=|x²-4x+3|在[0,3]上的最大值", "等差数列首项4，公差3，求第99999999项"} {
		if _, err := RadarVariant(View{Question: Question{Stem: stem}}, 1); err == nil {
			t.Fatal("unsupported accepted", stem)
		}
	}
	if _, err := RadarVariant(View{Question: Question{Stem: "解方程4x-7=13"}}, 0); err == nil {
		t.Fatal("invalid seed accepted")
	}
}

func TestRadarDoesNotMistakeConjunctionForSum(t *testing.T) {
	c, err := RadarVariant(View{Question: Question{Stem: "等差数列首项为4和公差为3，求第8项"}}, 1)
	if err != nil || len(c.Solution.Steps) != 2 {
		t.Fatalf("unexpected summation: %+v %v", c, err)
	}
}
