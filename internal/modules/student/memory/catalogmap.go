package memory

// solveCatalogToTaxonomy maps the teaching/solve module's knowledge catalog
// codes onto the memory taxonomy's knowledge-area codes. The two modules use
// independent code sets, so a deterministic map is the single place they are
// reconciled. An unknown solve code maps to "" and is dropped, never guessed.
//
// solve codes (8):
//
//	sets_logic, functions_derivatives, sequences, trigonometry,
//	analytic_geometry, solid_geometry, probability_statistics,
//	equations_inequalities
var solveCatalogToTaxonomy = map[string]string{
	"sets_logic":             "ALG",      // 集合与逻辑 -> 代数与方程
	"equations_inequalities": "ALG",      // 方程与不等式 -> 代数与方程
	"functions_derivatives":  "FUNC",     // 函数与导数 -> 函数与图象
	"sequences":              "SEQ",      // 数列
	"trigonometry":           "TRIG",     // 三角函数
	"analytic_geometry":      "ANALYTIC", // 平面解析几何
	"solid_geometry":         "SOLID",    // 立体几何
	"probability_statistics": "PROB",     // 概率与统计
}

// MapSolveKnowledge resolves a solve catalog code to a memory taxonomy area
// code. It returns the empty string when the code is unknown so callers drop it
// instead of fabricating a taxonomy code.
func MapSolveKnowledge(solveCode string) string {
	return solveCatalogToTaxonomy[solveCode]
}
