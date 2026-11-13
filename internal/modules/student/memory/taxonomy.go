package memory

import (
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// Taxonomy is the standardized classification whitelist for L2 mastery
// profiling. Extractors may only reference codes registered here.
type Taxonomy struct {
	Areas     map[string]string // code -> name
	Abilities map[string]string // code -> name
}

// DefaultTaxonomy mirrors agent-design/contracts/knowledge_taxonomy.json and is
// the single source of truth for server-side validation.
func DefaultTaxonomy() Taxonomy {
	return Taxonomy{
		Areas: map[string]string{
			"ALG":      "代数与方程",
			"FUNC":     "函数与图象",
			"SEQ":      "数列",
			"TRIG":     "三角函数",
			"VEC":      "平面向量",
			"SOLID":    "立体几何",
			"ANALYTIC": "解析几何",
			"PROB":     "概率与统计",
			"CALC":     "导数与微积分",
		},
		Abilities: map[string]string{
			"AB-COMPUTE": "运算求解",
			"AB-REASON":  "逻辑推理",
			"AB-MODEL":   "数学建模",
			"AB-SPACE":   "直观想象",
			"AB-DATA":    "数据分析",
		},
	}
}

var validMastery = map[string]bool{
	MasteryUnseen:   true,
	MasteryFamiliar: true,
	MasteryPartial:  true,
	MasteryMastered: true,
}

// ValidArea reports whether the code is a registered knowledge area.
func ValidArea(code string) bool { _, ok := DefaultTaxonomy().Areas[code]; return ok }

// ValidAbility reports whether the code is a registered ability dimension.
func ValidAbility(code string) bool { _, ok := DefaultTaxonomy().Abilities[code]; return ok }

// ValidMastery reports whether the code is a registered mastery level.
func ValidMastery(code string) bool { return validMastery[code] }

// MasteryRank orders mastery levels: unseen < familiar < partial < mastered.
func MasteryRank(code string) int {
	switch code {
	case MasteryMastered:
		return 3
	case MasteryPartial:
		return 2
	case MasteryFamiliar:
		return 1
	default:
		return 0
	}
}

// Forgetting-dimension constants (HLR simplified model). See
// docs/学生档案遗忘维度设计.md. These are server-side deterministic values;
// extractors never supply them.
const (
	// DefaultStability is the neutral prior for a newly seen knowledge point.
	DefaultStability = 0.5
	// DefaultDifficulty is the neutral prior for a new knowledge point.
	DefaultDifficulty = 0.2
	// RecallThreshold triggers a "due for review" flag when retrievability drops below it.
	RecallThreshold = 0.80
	// HalfLifeBase days plus a stability/difficulty-scaled term bound the decay half-life.
	HalfLifeBase = 1.0
	// HalfLifeScale is the maximum half-life in days at stability=1, difficulty=0.
	HalfLifeScale = 14.0
	// DifficultySlope maps each error_count unit to an increase in difficulty.
	DifficultySlope = 0.08
)

// UpdateStability advances a topic's memory stability from its current mastery
// outcome. Only student-authored turns change it; unseen (demonstration/answer
// view) leaves it unchanged. Returned value is clamped to [0,1].
func UpdateStability(stability float64, mastery string) float64 {
	var delta float64
	switch mastery {
	case MasteryMastered:
		delta = 0.15
	case MasteryPartial:
		delta = 0.05
	case MasteryFamiliar:
		delta = -0.10
	default: // unseen
		return clamp01(stability)
	}
	return clamp01(stability + delta)
}

// UpdateDifficulty derives a topic's difficulty from its error count. Higher
// error counts mean faster forgetting, so difficulty rises monotonically.
func UpdateDifficulty(errorCount int) float64 {
	if errorCount < 0 {
		errorCount = 0
	}
	return clamp01(DefaultDifficulty + DifficultySlope*float64(errorCount))
}

// Retrievability computes the probability a topic is still recallable at
// elapsed days after lastSeen, using an Ebbinghaus-style exponential decay whose
// half-life is jointly determined by stability and difficulty. It is derived at
// read time and never written back to the source document.
func Retrievability(stability, difficulty float64, elapsed time.Duration) float64 {
	days := elapsed.Hours() / 24
	if days < 0 {
		days = 0
	}
	stability = clamp01(stability)
	difficulty = clamp01(difficulty)
	halfLife := HalfLifeBase + HalfLifeScale*stability*(1-difficulty)
	if halfLife <= 0 {
		halfLife = HalfLifeBase
	}
	return math.Pow(0.5, days/halfLife)
}

// IsDue reports whether a topic should be surfaced for review: its current
// retrievability has dropped below the recall threshold.
func IsDue(retrievability float64) bool {
	return retrievability < RecallThreshold
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// ValidateSummaryContent checks the L2 mastery profile against size and taxonomy
// constraints. It never trusts extractor-supplied codes without whitelisting.
func ValidateSummaryContent(c SummaryContent) error {
	if strings.TrimSpace(c.Summary) == "" || utf8.RuneCountInString(c.Summary) > 2000 {
		return errors.New("student memory: invalid summary")
	}
	if len(c.KnowledgeAreas) > 9 {
		return errors.New("student memory: too many knowledge areas")
	}
	total := 0
	seen := map[string]bool{}
	for _, area := range c.KnowledgeAreas {
		if !ValidArea(area.Area) {
			return errors.New("student memory: unregistered knowledge area")
		}
		if seen[area.Area] {
			return errors.New("student memory: duplicate knowledge area")
		}
		seen[area.Area] = true
		if len(area.Topics) > 50 {
			return errors.New("student memory: too many topics in area")
		}
		for _, t := range area.Topics {
			if strings.TrimSpace(t.Topic) == "" || utf8.RuneCountInString(t.Topic) > 60 {
				return errors.New("student memory: invalid topic")
			}
			if !ValidMastery(t.Mastery) {
				return errors.New("student memory: invalid mastery level")
			}
			if !ValidAbility(t.Ability) {
				return errors.New("student memory: unregistered ability")
			}
			if t.ErrorCount < 0 {
				return errors.New("student memory: negative error count")
			}
			if t.Stability < 0 || t.Stability > 1 {
				return errors.New("student memory: stability out of range")
			}
			if t.Difficulty < 0 || t.Difficulty > 1 {
				return errors.New("student memory: difficulty out of range")
			}
			if len(t.QuestionIDs) > 50 {
				return errors.New("student memory: too many question ids")
			}
			total++
		}
	}
	if total > 450 {
		return errors.New("student memory: monthly topic capacity exceeded")
	}
	return nil
}
