package solve

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var ErrRadarUnsupported = errors.New("题目雷达暂不支持对这道原题进行严格换数值校验，请换一个已学知识点")
var radarLinear = regexp.MustCompile(`(?:^|[^a-zA-Z0-9])([+-]?\d*)x([+-]\d+)=([+-]?\d+)`)
var radarQuadratic = regexp.MustCompile(`f\(x\)=([+-]?\d*)x\^2([+-]\d*)x([+-]\d+)(?:$|[，。；在的])`)
var radarInterval = regexp.MustCompile(`\[(-?\d+)[,，](-?\d+)\]`)
var radarFirst = regexp.MustCompile(`首项[^0-9-]*(-?\d+)`)
var radarDifference = regexp.MustCompile(`公差[^0-9-]*(-?\d+)`)
var radarTerm = regexp.MustCompile(`第\s*(\d+)\s*项`)

// RadarVariant uses small integer parameters and independently verifies every
// numeric result. It never accepts a model's self-reported mathematical proof.
func RadarVariant(source View, seed int) (Candidate, error) {
	if seed < 1 || seed > 1000000000 {
		return Candidate{}, ErrInvalid
	}
	stem := source.Question.Stem
	compact := strings.NewReplacer(" ", "", "−", "-", "－", "-", "＋", "+", "²", "^2").Replace(stem)
	var question, answer, analysis, teacher, code string
	var steps []Step
	add := func(title, content string) {
		steps = append(steps, Step{Ordinal: len(steps) + 1, Title: title, Content: content})
	}
	switch {
	case strings.Contains(stem, "等差") && radarFirst.MatchString(stem) && radarDifference.MatchString(stem) && radarTerm.MatchString(stem):
		originalA, _ := strconv.Atoi(radarFirst.FindStringSubmatch(stem)[1])
		originalD, _ := strconv.Atoi(radarDifference.FindStringSubmatch(stem)[1])
		originalN, _ := strconv.Atoi(radarTerm.FindStringSubmatch(stem)[1])
		if originalN < 1 || originalN > 1000 || originalA > 1000 || originalA < -1000 || originalD > 1000 || originalD < -1000 {
			return Candidate{}, ErrRadarUnsupported
		}
		a := originalA + 1 + seed%5
		d := originalD + 1 + seed%3
		if d == 0 {
			d = 2
		}
		n := originalN + 1 + seed%3
		value := a + (n-1)*d
		check := a
		for i := 1; i < n; i++ {
			check += d
		}
		if check != value {
			return Candidate{}, errors.New("等差数列独立验算失败")
		}
		wantSum := strings.Contains(stem, "项和") || strings.Contains(stem, "求和") || strings.Contains(stem, "S_") || strings.Contains(stem, "S₁")
		question = fmt.Sprintf("等差数列的首项为%d，公差为%d，求第%d项。", a, d, n)
		answer = fmt.Sprintf("第%d项为%d。", n, value)
		add("确定间隔数", fmt.Sprintf("首项到第%d项共有%d个间隔。", n, n-1))
		add("代入通项公式", fmt.Sprintf("aₙ=a₁+(n−1)d，因此a%d=%d+(%d−1)×%d=%d。", n, a, n, d, value))
		if wantSum {
			sum := n * (a + value) / 2
			loop := 0
			for i := 0; i < n; i++ {
				loop += a + i*d
			}
			if sum != loop {
				return Candidate{}, errors.New("数列求和验算失败")
			}
			question = fmt.Sprintf("等差数列的首项为%d，公差为%d，求第%d项和前%d项和。", a, d, n, n)
			answer += fmt.Sprintf("前%d项和为%d。", n, sum)
			add("计算前n项和", fmt.Sprintf("S%d=%d×(%d+%d)÷2=%d。", n, n, a, value, sum))
		}
		code = "sequences"
		analysis = "先区分项数与间隔数，再使用等差数列通项公式；要求和时用首末项平均值乘项数。"
	case radarQuadratic.MatchString(compact) && radarInterval.MatchString(compact) && !strings.Contains(compact, "|") && (strings.Contains(stem, "最大") || strings.Contains(stem, "最小") || strings.Contains(stem, "最值")) && (strings.Contains(stem, "区间") || strings.Contains(stem, "[")):
		a := 1 + seed%3
		v := 1 + seed%4
		k := seed%9 - 4
		left := v - (1 + seed%3)
		right := v + (2 + seed%4)
		b := -2 * a * v
		c := a*v*v + k
		old := radarQuadratic.FindStringSubmatch(compact)
		coeff := func(v string) int {
			if v == "" || v == "+" {
				return 1
			}
			if v == "-" {
				return -1
			}
			n, _ := strconv.Atoi(v)
			return n
		}
		if a == coeff(old[1]) && b == coeff(old[2]) && c == coeff(old[3]) {
			k++
			c++
		}
		f := func(x int) int { return a*x*x + b*x + c }
		minimum := f(v)
		l, r := f(left), f(right)
		maximum := l
		if r > maximum {
			maximum = r
		}
		if minimum != k || b+2*a*v != 0 || v < left || v > right {
			return Candidate{}, errors.New("二次函数顶点验算失败")
		}
		for x := left; x <= right; x++ {
			if f(x) < minimum || f(x) > maximum {
				return Candidate{}, errors.New("二次函数区间验算失败")
			}
		}
		question = fmt.Sprintf("求函数 f(x)=%dx²%+dx%+d 在区间[%d,%d]上的最大值和最小值。", a, b, c, left, right)
		answer = fmt.Sprintf("最大值为%d，最小值为%d。", maximum, minimum)
		code = "functions_derivatives"
		analysis = "开口向上的二次函数在包含顶点的闭区间上，最小值在顶点取得；最大值比较两个端点。"
		add("确定顶点", fmt.Sprintf("f(x)=%d(x−%d)²%+d，开口向上，顶点为(%d,%d)，且顶点在区间内。", a, v, k, v, k))
		add("比较关键点", fmt.Sprintf("f(%d)=%d，f(%d)=%d，f(%d)=%d，因此%s", left, l, v, minimum, right, r, answer))
	case radarLinear.MatchString(compact) && !strings.Contains(compact, "^2"):
		matches := radarLinear.FindStringSubmatch(compact)
		oldA := matches[1]
		if oldA == "" || oldA == "+" {
			oldA = "1"
		}
		if oldA == "-" {
			oldA = "-1"
		}
		originalA, _ := strconv.Atoi(oldA)
		if originalA == 0 {
			return Candidate{}, ErrRadarUnsupported
		}
		a := 2 + seed%7
		root := 1 + seed%9
		b := seed%13 - 6
		if b == 0 {
			b = 3
		}
		c := a*root + b
		oldB, _ := strconv.Atoi(matches[2])
		oldC, _ := strconv.Atoi(matches[3])
		if a == originalA && b == oldB && c == oldC {
			b++
			c++
		}
		if a == 0 || a*root+b != c || (c-b)%a != 0 {
			return Candidate{}, errors.New("一元一次方程代回验算失败")
		}
		question = fmt.Sprintf("解方程：%dx%+d=%d。", a, b, c)
		answer = fmt.Sprintf("x=%d。", root)
		code = "equations_inequalities"
		analysis = "利用等式两边同加减、同除以非零数，将未知数的系数化为1。"
		add("消去常数项", fmt.Sprintf("等式两边同时减去%d，得到%dx=%d。", b, a, c-b))
		add("系数化为1并检验", fmt.Sprintf("两边同时除以%d，得x=%d；代回原式：%d×%d%+d=%d，等式成立。", a, root, a, root, b, c))
	default:
		return Candidate{}, ErrRadarUnsupported
	}
	if question == stem {
		return Candidate{}, errors.New("变式数值未改变")
	}
	teacher = "这道题的关键是：" + analysis + "\n\n"
	for _, step := range steps {
		teacher += fmt.Sprintf("**%s**\n\n%s\n\n", step.Title, step.Content)
	}
	teacher += "因此，" + answer
	candidate := Candidate{Question: Question{Stem: question, Type: "constructed_response", Difficulty: source.Question.Difficulty, Options: []Option{}, Subquestions: []Subquestion{}}, Solution: Solution{TeacherResponse: teacher, Answer: []Answer{{Content: answer}}, Analysis: analysis, Steps: steps, KnowledgePoints: []Knowledge{{Code: code, IsPrimary: true}}}}
	if candidate.Question.Difficulty != "easy" && candidate.Question.Difficulty != "medium" && candidate.Question.Difficulty != "hard" {
		candidate.Question.Difficulty = "easy"
	}
	return candidate, Validate(candidate)
}

func CheckRadarCandidate(source View, seed int, candidate Candidate) error {
	expected, err := RadarVariant(source, seed)
	if err != nil {
		return err
	}
	a, _ := json.Marshal(expected)
	b, _ := json.Marshal(candidate)
	if string(a) != string(b) {
		return errors.New("严格校验未通过：题干、数值、答案、步骤或讲解与可验算变式不一致，请原样提交verify_question_variant返回的candidate")
	}
	return nil
}
