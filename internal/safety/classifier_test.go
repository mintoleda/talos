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
		{"python3 -c 'open(\"x.go\",\"w\")'", Prompt, false},
		{"python -c 'print(1)'", Prompt, false},
		{"node -e 'require(\"fs\").writeFileSync(\"x\",\"y\")'", Prompt, false},
		{"ruby -e 'File.write'", Prompt, false},
		{"perl -e 'open'", Prompt, false},
		{"cat file.txt > out.go", Prompt, false},
		{"cat input.txt > output.py", Prompt, false},
		{"sed -i 's/a/b/' src/main.go", Prompt, false},
		{"cat <<EOF > src/config.toml\nhi\nEOF", Prompt, false},
		{"echo done > main.rs", Prompt, false},
		{"python myscript.py", Allow, false},
		{"cat README.md", Allow, false},
		{"echo hello", Allow, false},
	}
	for _, tc := range cases {
		d, _ := c.Classify(tc.cmd)
		if d != tc.want {
			t.Errorf("%q: got %d, want %d", tc.cmd, d, tc.want)
		}
	}
}
