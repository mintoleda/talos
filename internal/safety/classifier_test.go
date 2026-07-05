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
		{"python3 -c 'open(\"x.go\",\"w\")'", Block, true},
		{"python -c 'print(1)'", Block, true},
		{"node -e 'require(\"fs\").writeFileSync(\"x\",\"y\")'", Block, true},
		{"ruby -e 'File.write'", Block, true},
		{"perl -e 'open'", Block, true},
		{"perl -pe 's/foo/bar/' file.go", Block, true},
		{"perl -pi -e 's/foo/bar/g' file.go", Block, true},
		{"perl -n -e 'print if /foo/' file.go", Block, true},
		{"ruby -pe 'gsub(/foo/,\"bar\")' file.go", Block, true},
		{"ruby -n -e 'puts if /foo/' file.go", Block, true},
		{"php -r 'echo 1;'", Block, true},
		{"cat file.txt > out.go", Block, true},
		{"cat input.txt > output.py", Block, true},
		{"sed -i 's/a/b/' src/main.go", Block, true},
		{"cat <<EOF > src/config.toml\nhi\nEOF", Block, true},
		{"echo done > main.rs", Block, true},
		{"echo 'content' | tee file.go", Block, true},
		{"dd if=/dev/zero of=file.go bs=1 count=1", Block, true},
		{"perl -i -pe 's/foo/bar/' src/main.go", Block, true},
		{"sed -i 's/a/b/' /tmp/scratch", Allow, false},
		{"python myscript.py", Allow, false},
		{"cat README.md", Allow, false},
		{"echo hello", Allow, false},
		{"php artisan serve", Allow, false},
		{"python3 manage.py runserver", Allow, false},
	}
	for _, tc := range cases {
		d, _ := c.Classify(tc.cmd)
		if d != tc.want {
			t.Errorf("%q: got %d, want %d", tc.cmd, d, tc.want)
		}
	}
}
