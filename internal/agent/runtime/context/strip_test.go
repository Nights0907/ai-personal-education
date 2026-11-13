package context

import "testing"

func TestStripTurnContextDropsStaleModeBlocks(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "solve block is dropped",
			in:   "<current-problem>\n{\"mode\":\"SOLVE\"}\n</current-problem>\n\n<student-input>\n解方程 x^2-5x+6=0\n</student-input>",
			want: "<student-input>\n解方程 x^2-5x+6=0\n</student-input>",
		},
		{
			name: "plain input is untouched",
			in:   "<student-input>\n我想因式分解\n</student-input>",
			want: "<student-input>\n我想因式分解\n</student-input>",
		},
		{
			name: "block without student section is left as is",
			in:   "<current-problem>\n{\"mode\":\"SOLVE\"}\n</current-problem>",
			want: "<current-problem>\n{\"mode\":\"SOLVE\"}\n</current-problem>",
		},
	}
	for _, c := range cases {
		if got := stripTurnContext(c.in); got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
