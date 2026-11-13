package memory

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

type TopicDefinition struct{ Area, Name string }

var TopicCatalog = map[string]TopicDefinition{
	"ALG_LINEAR": {"ALG", "一元一次方程"}, "ALG_QUADRATIC": {"ALG", "一元二次方程"}, "ALG_INEQUALITY": {"ALG", "不等式"},
	"FUNC_QUADRATIC": {"FUNC", "二次函数"}, "FUNC_MONOTONIC": {"FUNC", "函数单调性"}, "FUNC_DOMAIN": {"FUNC", "函数定义域"},
	"SEQ_ARITHMETIC": {"SEQ", "等差数列"}, "SEQ_GEOMETRIC": {"SEQ", "等比数列"},
	"TRIG_IDENTITIES": {"TRIG", "三角恒等变换"}, "VEC_OPERATIONS": {"VEC", "向量运算"},
	"SOLID_GEOMETRY": {"SOLID", "空间几何"}, "ANALYTIC_CONIC": {"ANALYTIC", "圆锥曲线"},
	"PROB_PROBABILITY": {"PROB", "概率"}, "PROB_STATISTICS": {"PROB", "统计"}, "CALC_DERIVATIVE": {"CALC", "导数"},
}

// Unknown legacy labels retain stable identities; they are never collapsed into
// one generic topic. Known synonyms migrate to the same registered identity.
func CanonicalTopicID(area, label, id string) string {
	if d, ok := TopicCatalog[id]; ok && d.Area == area {
		return id
	}
	for _, v := range []struct{ word, id string }{{"一元二次", "ALG_QUADRATIC"}, {"一元一次", "ALG_LINEAR"}, {"不等式", "ALG_INEQUALITY"}, {"二次函数", "FUNC_QUADRATIC"}, {"单调", "FUNC_MONOTONIC"}, {"定义域", "FUNC_DOMAIN"}, {"等差", "SEQ_ARITHMETIC"}, {"等比", "SEQ_GEOMETRIC"}, {"三角", "TRIG_IDENTITIES"}, {"向量", "VEC_OPERATIONS"}, {"空间", "SOLID_GEOMETRY"}, {"圆锥", "ANALYTIC_CONIC"}, {"概率", "PROB_PROBABILITY"}, {"统计", "PROB_STATISTICS"}, {"导数", "CALC_DERIVATIVE"}} {
		if strings.Contains(label, v.word) && TopicCatalog[v.id].Area == area {
			return v.id
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(label), "")))
	return fmt.Sprintf("%s_LEGACY_%x", area, sum[:8])
}
