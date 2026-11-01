package pluginconf

import (
	"fmt"
	"net/url"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/TechDufus/dash/internal/model"
)

// Launcher is plugins.launcher: the launcher's list of infrastructure links, from a file or
// inline, not both.
type Launcher struct {
	PluginBase
	File  string               `json:"file" doc:"Path of a YAML file with the launcher's links (see docs/launcher.example.yaml). Not together with links."`
	Links []model.LauncherLink `json:"links" doc:"The launcher's links, inline: a list of {name, href, icon, description}. Not together with file."`
}

// Validate checks the static rules: file and links are not both set, and every inline link has a
// name and an http(s) href. It reads no file; LoadLauncher does.
func (l Launcher) Validate() error {
	var p problems
	if l.File != "" && len(l.Links) > 0 {
		p.add("links", "cannot be set together with plugins.launcher.file")
	}
	if err := validateLinks(l.Links); err != nil {
		p.add("links", "%v", err)
	}
	return p.err()
}

// LoadLauncher reads a YAML or JSON list of launcher links.
func LoadLauncher(path string) ([]model.LauncherLink, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("launcher file: %w", err)
	}
	var links []model.LauncherLink
	if err := yaml.UnmarshalStrict(raw, &links); err != nil {
		return nil, fmt.Errorf("launcher file %s: %w", path, err)
	}
	if err := validateLinks(links); err != nil {
		return nil, fmt.Errorf("launcher file %s: %w", path, err)
	}
	return links, nil
}

// validateLinks checks launcher entries; the error counts from 1.
func validateLinks(links []model.LauncherLink) error {
	for i, l := range links {
		u, err := url.Parse(l.Href)
		if l.Name == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("entry %d needs a name and an http(s) href", i+1)
		}
	}
	return nil
}
