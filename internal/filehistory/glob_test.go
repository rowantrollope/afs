package filehistory

import (
	"context"
	"reflect"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestOriginalGlobGrammarParity(t *testing.T) {
	rdb := testHistory(t)
	script := redis.NewScript(globLua + `return history_glob(ARGV[1],ARGV[2]) and 1 or 0`)
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"src/**/*.go", "src/main.go", true}, {"src/**/*.go", "src/a/b/main.go", true},
		{"src/**", "src", true}, {"src/**", "src/a/b", true},
		{"src/a**b", "src/a/b", false}, {"src/a**b", "src/aaab", true},
		{"**/[a-c]?.[ch]", "a/b/cx.h", true}, {"**/[a-c]?.[ch]", "a/b/dx.h", false},
		{"[^ab].go", "c.go", true}, {"[^ab].go", "a.go", false},
		{`docs/\[draft\].md`, "docs/[draft].md", true},
		{`docs/\*.md`, "docs/*.md", true}, {`docs/\*.md`, "docs/a.md", false},
		{"café/[é-ê]?.txt", "café/ê字.txt", true}, {"café/[é-ê]?.txt", "café/e字.txt", false},
		{" /src/**/*.go/ ", " /src/main.go/ ", true},
		{"**/**/**/[a-z]*", "x/y/z/abc", true}, {"a//b", "a//b", true},
		{"*", "a/b", false}, {"**", "a/b", true}, {"?", "字", true},
		{"[!a]", "!", true}, {"[!a]", "z", false},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+"/"+tc.path, func(t *testing.T) {
			glob, err := compileGlob(tc.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if got := glob.MatchString(tc.path); got != tc.want {
				t.Fatalf("Go got %v, want %v", got, tc.want)
			}
			got, err := script.Run(context.Background(), rdb, nil, tc.pattern, tc.path).Int()
			if err != nil || (got == 1) != tc.want {
				t.Fatalf("Lua got %d, want %v: %v", got, tc.want, err)
			}
		})
	}
}

func TestOriginalPolicyNormalization(t *testing.T) {
	rdb := testHistory(t)
	input := Policy{Mode: " PATHS ", Include: []string{" src/** ", "src/**", "", "pkg/[a-z]*"}, Exclude: []string{" **/*.log ", "**/*.log"}}
	if err := SetPolicy(context.Background(), rdb, "test", input); err != nil {
		t.Fatal(err)
	}
	got, err := GetPolicy(context.Background(), rdb, "test")
	want := Policy{Mode: ModePaths, Include: []string{"src/**", "pkg/[a-z]*"}, Exclude: []string{"**/*.log"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v: %v", got, want, err)
	}
}
