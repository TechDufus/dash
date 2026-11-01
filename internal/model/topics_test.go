package model

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

func readSPA(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../web/src/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// declaredTopics returns what the SPA declares as `name: topic<Type>()`: the core topics in
// plugins/topic-data.ts and each module's `topics` in plugins/modules/<id>/index.ts[x].
func declaredTopics(t *testing.T) (names, payloads []string) {
	t.Helper()
	decl := regexp.MustCompile(`"?([\w.]+)"?: topic<(\w+)>\(\)`)
	files := []string{"../../web/src/plugins/topic-data.ts"}
	for _, ext := range []string{"ts", "tsx"} {
		more, err := filepath.Glob("../../web/src/plugins/modules/*/index." + ext)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, more...)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range decl.FindAllStringSubmatch(string(b), -1) {
			names = append(names, m[1])
			payloads = append(payloads, m[1]+":"+m[2])
		}
	}
	return names, payloads
}

// The topic constants, TopicTypes and the topics the SPA declares (core topics and module
// `topics`) name the same topics, and each one's payload type is the one the SPA reads (a rename
// lands in Go and web together). The TypeScript compiler checks the same thing from its side
// (_topicsExact in plugins/topic-data.ts); this test names the difference.
func TestTopicsAgreeInGoAndWeb(t *testing.T) {
	var constants []string
	for _, m := range regexp.MustCompile(`(?m)^export const Topic\w+ = "([^"]+)";`).FindAllStringSubmatch(readSPA(t, "api/types.ts"), -1) {
		constants = append(constants, m[1])
	}

	var typed []string
	for topic := range TopicTypes() {
		typed = append(typed, string(topic))
	}

	data, payload := declaredTopics(t)
	for name, got := range map[string][]string{"TopicTypes": typed, "TopicData": data} {
		a, b := slices.Clone(constants), slices.Clone(got)
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			t.Errorf("%s = %v, but the Topic constants are %v", name, b, a)
		}
	}

	var want []string
	for topic, typ := range TopicTypes() {
		want = append(want, string(topic)+":"+typ.Name())
	}
	slices.Sort(want)
	slices.Sort(payload)
	if !slices.Equal(want, payload) {
		t.Errorf("TopicData payloads = %v, want %v", payload, want)
	}
}
