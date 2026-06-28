package safety

import "testing"

func TestClassifier(t *testing.T) {
	c := NewClassifier()
	cases := []struct {
		cmd       string
		want      Decision
		mustBlock bool
	}{
		{"rm -rf /", Block, true},
		{"rm -rf ~", Block, true},
		{"rm performance.txt", Allow, false},
		{"sudo apt update", Prompt, false},
		{"git status", Allow, false},
		{"git checkout -- .", Prompt, false},
		{"git restore foo.go", Allow, false},
	}
	for _, tc := range cases {
		d, _ := c.Classify(tc.cmd)
		if d != tc.want {
			t.Errorf("%q: got %d, want %d", tc.cmd, d, tc.want)
		}
	}
}
