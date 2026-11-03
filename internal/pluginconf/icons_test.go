package pluginconf

import (
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
)

func TestValidateIcons(t *testing.T) {
	const cdn = "https://cdn.example.com/png"
	for name, tc := range map[string]struct {
		icon, base string
		ok         bool
	}{
		"empty":                       {"", cdn, true},
		"empty without a base":        {"", "", true},
		"name":                        {"proxmox", cdn, true},
		"name without a base":         {"proxmox", "", true},
		"name with a dash":            {"unifi-drive", cdn, true},
		"name with dots":              {"grafana.v2.svg", cdn, true},
		"name of 64 characters":       {strings.Repeat("a", 64), cdn, true},
		"name of 65 characters":       {strings.Repeat("a", 65), cdn, false},
		"name starting with a dot":    {".hidden", cdn, false},
		"name starting with a dash":   {"-x", cdn, false},
		"protocol-relative":           {"//evil/x.png", cdn, false},
		"protocol-relative no base":   {"//evil/x.png", "", false},
		"same-origin path":            {"/icons/x.svg", cdn, true},
		"same-origin path no base":    {"/icons/x.svg", "", true},
		"path with a backslash":       {`/icons\x.svg`, cdn, false},
		"slash-backslash":             {`/\evil/x.png`, cdn, false},
		"path with a tab":             {"/\t/evil/x.png", cdn, false},
		"path with a newline":         {"/icons/x\n.png", cdn, false},
		"path with DEL":               {"/icons/x\x7f.png", cdn, false},
		"url under the base":          {cdn + "/x.png", cdn, true},
		"url under the base, slash":   {cdn + "/x.png", cdn + "/", true},
		"url under a deeper path":     {cdn + "/sub/x.png", cdn, true},
		"url with a query":            {cdn + "/x.png?v=2", cdn, true},
		"uppercase scheme and host":   {"HTTPS://CDN.EXAMPLE.COM/png/x.png", cdn, true},
		"explicit default port":       {"https://cdn.example.com:443/png/x.png", cdn, true},
		"origin-only base":            {"https://cdn.example.com/x.png", "https://cdn.example.com", true},
		"other host":                  {"https://evil.example.com/png/x.png", cdn, false},
		"subdomain trick":             {"https://cdn.example.com.evil.net/png/x.png", cdn, false},
		"different port":              {"https://cdn.example.com:8443/png/x.png", cdn, false},
		"different scheme":            {"http://cdn.example.com/png/x.png", cdn, false},
		"base with a port":            {"https://cdn.example.com:8443/png/x.png", "https://cdn.example.com:8443/png", true},
		"base port, url without":      {"https://cdn.example.com/png/x.png", "https://cdn.example.com:8443/png", false},
		"path-prefix trick":           {"https://cdn.example.com/pngx/x.png", cdn, false},
		"path-prefix trick same-orig": {"/pngx/x.png", "/png", true},
		"base path itself":            {cdn, cdn, false},
		"outside the base path":       {"https://cdn.example.com/other/x.png", cdn, false},
		"dot-dot out of the base":     {cdn + "/../evil/x.png", cdn, false},
		"encoded dot-dot":             {cdn + "/%2e%2e/evil/x.png", cdn, false},
		"encoded backslash":           {cdn + "/%5cevil/x.png", cdn, false},
		"credentials":                 {"https://user:pw@cdn.example.com/png/x.png", cdn, false},
		"url with a backslash":        {`https://cdn.example.com/png\x.png`, cdn, false},
		"url with a control char":     {"https://cdn.example.com/png/x\t.png", cdn, false},
		"url without a base":          {cdn + "/x.png", "", false},
		"url with a non-http base":    {cdn + "/x.png", "ftp://cdn.example.com/png", false},
		"url with an invalid base":    {cdn + "/x.png", "://nope", false},
		"javascript":                  {"javascript:alert(1)", cdn, false},
		"javascript with a base-like": {"javascript://cdn.example.com/png/%0aalert(1)", cdn, false},
		"data":                        {"data:image/svg+xml,<svg/>", cdn, false},
		"data with a base":            {"data:image/png;base64,AAAA", "data:", false},
		"relative path":               {"icons/x.png", cdn, false},
		"mailto":                      {"mailto:a@example.com", cdn, false},
		"surrounding spaces":          {"  proxmox  ", cdn, true},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateIcons([]model.LauncherLink{{Name: "n", Href: "https://x", Icon: tc.icon}}, tc.base)
			if tc.ok && err != nil {
				t.Fatalf("icon %q with base %q: %v, want valid", tc.icon, tc.base, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("icon %q with base %q is valid, want an error", tc.icon, tc.base)
			}
		})
	}
}

// The error names the entry (counting from 1) and never repeats the token: a URL can carry a
// query or credentials that must not reach the log.
func TestValidateIconsErrorNamesTheEntryAndLeaksNothing(t *testing.T) {
	links := []model.LauncherLink{
		{Name: "ok", Href: "https://a", Icon: "proxmox"},
		{Name: "no icon", Href: "https://b"},
		{Name: "bad", Href: "https://c", Icon: "https://user:hunter2@evil.example.com/x.png?token=s3cret"},
	}
	err := ValidateIcons(links, "https://cdn.example.com/png")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.HasPrefix(err.Error(), "entry 3: icon ") {
		t.Errorf("error %q does not start with %q", err, "entry 3: icon ")
	}
	for _, leak := range []string{"hunter2", "s3cret", "token=", "evil.example.com"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q repeats %q", err, leak)
		}
	}
}

func TestValidateIconsReportsTheFirstBadEntry(t *testing.T) {
	links := []model.LauncherLink{
		{Name: "a", Href: "https://a", Icon: "javascript:alert(1)"},
		{Name: "b", Href: "https://b", Icon: "data:image/png;base64,AAAA"},
	}
	err := ValidateIcons(links, "")
	if err == nil || !strings.HasPrefix(err.Error(), "entry 1: icon ") {
		t.Fatalf("error = %v, want entry 1", err)
	}
	if err := ValidateIcons(nil, ""); err != nil {
		t.Errorf("no links: %v", err)
	}
}
