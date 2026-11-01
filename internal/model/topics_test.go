package model

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

func readSPA(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../web/src/api/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The topic constants, TopicTypes and the SPA's TopicData name the same topics, and
// each one's payload type is the one the SPA reads (a rename lands in Go and web together).
func TestTopicsAgreeInGoAndWeb(t *testing.T) {
	var constants []string
	for _, m := range regexp.MustCompile(`(?m)^export const Topic\w+ = "([^"]+)";`).FindAllStringSubmatch(readSPA(t, "types.ts"), -1) {
		constants = append(constants, m[1])
	}

	var typed []string
	for topic := range TopicTypes() {
		typed = append(typed, string(topic))
	}

	topicsTS := readSPA(t, "topics.ts")
	iface := regexp.MustCompile(`(?s)export interface TopicData \{(.*?)\n\}`).FindStringSubmatch(topicsTS)
	if iface == nil {
		t.Fatal("topics.ts has no TopicData interface")
	}
	var data, payload []string
	for _, m := range regexp.MustCompile(`(?m)^\s+"?([\w.]+)"?: (\w+);`).FindAllStringSubmatch(iface[1], -1) {
		data = append(data, m[1])
		payload = append(payload, m[1]+":"+m[2])
	}
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
