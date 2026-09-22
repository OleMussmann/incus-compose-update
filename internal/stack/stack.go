// Package stack reads a stack directory: its rendered compose definition, the
// x-update: metadata on each service, and the pin variable in the raw file.
//
// Metadata comes from the *rendered* definition (`incus-compose config`), not
// the raw compose.yaml: incus-compose merges overlay files (a gitignored
// compose.incus.yaml, say), so the raw base file is not what gets deployed,
// and the render comes from the same binary that deploys. x-update: blocks
// survive the render intact. The one thing rendering destroys is the ${VAR}
// pin variable name in image:, so that alone is read from the raw file.
package stack

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Dirs returns the stack directories from STACK_DIRS (colon-separated) or
// STACK_DIR.
func Dirs() ([]string, error) {
	if v := os.Getenv("STACK_DIRS"); v != "" {
		var dirs []string
		for _, d := range strings.Split(v, ":") {
			if d != "" {
				dirs = append(dirs, d)
			}
		}
		return dirs, nil
	}
	if v := os.Getenv("STACK_DIR"); v != "" {
		return []string{v}, nil
	}
	return nil, errors.New("set STACK_DIRS (colon-separated) or STACK_DIR")
}

// Rendered is a parsed `incus-compose config` output.
type Rendered struct {
	Name     string
	Services []*Service // in render order
}

// Service is one rendered service.
type Service struct {
	Name      string
	Image     string
	DependsOn []string
	// Managed reports whether the service carries an x-update: block.
	Managed bool
	update  rawUpdate
}

type rawUpdate struct {
	Mode       string   `yaml:"mode"`
	TagRE      string   `yaml:"tag_re"`
	Order      string   `yaml:"order"`
	Volume     string   `yaml:"volume"`
	VersionCmd []string `yaml:"version_cmd"`
}

// Update is a validated x-update: block with defaults applied.
type Update struct {
	Mode       string // "tag" | "digest"
	TagRE      *regexp.Regexp
	Order      string // "version" | "pushed"
	Volume     string // volume name, or "none"
	VersionCmd []string
}

// Service looks a service up by name.
func (r *Rendered) Service(name string) *Service {
	for _, s := range r.Services {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// Update validates the service's x-update: block and fills in defaults.
func (s *Service) Update() (*Update, error) {
	if !s.Managed {
		return nil, fmt.Errorf("service '%s' has no x-update: block", s.Name)
	}
	u := &Update{
		Mode:       orDefault(s.update.Mode, "tag"),
		Order:      orDefault(s.update.Order, "version"),
		Volume:     orDefault(s.update.Volume, "none"),
		VersionCmd: s.update.VersionCmd,
	}
	if len(u.VersionCmd) == 0 {
		u.VersionCmd = []string{s.Name, "--version"}
	}
	switch u.Mode {
	case "tag":
		if s.update.TagRE == "" {
			return nil, fmt.Errorf("service '%s': x-update.tag_re is required for mode: tag", s.Name)
		}
		re, err := regexp.Compile(s.update.TagRE)
		if err != nil {
			return nil, fmt.Errorf("service '%s': x-update.tag_re: %w", s.Name, err)
		}
		u.TagRE = re
	case "digest":
	default:
		return nil, fmt.Errorf("service '%s': x-update.mode must be tag or digest, got %q", s.Name, u.Mode)
	}
	if u.Order != "version" && u.Order != "pushed" {
		return nil, fmt.Errorf("service '%s': x-update.order must be version or pushed, got %q", s.Name, u.Order)
	}
	return u, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ParseRendered parses `incus-compose config` output. incus-compose 1.1.0
// wrapped the render in a top-level `project:` key; later versions emit
// name:/services: at the root. Both are accepted.
func ParseRendered(data []byte) (*Rendered, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse rendered config: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, errors.New("rendered config is empty")
	}
	root := doc.Content[0]
	if p := mapValue(root, "project"); p != nil && p.Kind == yaml.MappingNode {
		root = p
	}
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("rendered config is not a mapping")
	}

	r := &Rendered{}
	if n := mapValue(root, "name"); n != nil {
		r.Name = n.Value
	}
	services := mapValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, errors.New("rendered config has no services")
	}
	for i := 0; i+1 < len(services.Content); i += 2 {
		name, body := services.Content[i].Value, services.Content[i+1]
		s := &Service{Name: name}
		if n := mapValue(body, "image"); n != nil {
			s.Image = n.Value
		}
		if n := mapValue(body, "depends_on"); n != nil {
			s.DependsOn = keysOrItems(n)
		}
		if n := mapValue(body, "x-update"); n != nil && !isNullOrFalse(n) {
			s.Managed = true
			if err := n.Decode(&s.update); err != nil {
				return nil, fmt.Errorf("service '%s': x-update: %w", name, err)
			}
		}
		r.Services = append(r.Services, s)
	}
	return r, nil
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// keysOrItems reads depends_on in either compose form: a mapping of service
// names to conditions, or a plain list.
func keysOrItems(n *yaml.Node) []string {
	var out []string
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			out = append(out, c.Value)
		}
	}
	return out
}

func isNullOrFalse(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || (n.Tag == "!!bool" && n.Value == "false"))
}

// DependencyOrder returns service names with every service after the ones it
// depends on, breaking ties by render order. ok is false on a depends_on
// cycle, in which case the render order is returned unchanged.
//
// Ordering is churn control: a service-scoped recreate of a dependency tears
// its dependents down, so updating dependents last means they survive the run.
func (r *Rendered) DependencyOrder() (names []string, ok bool) {
	index := map[string]int{}
	for i, s := range r.Services {
		index[s.Name] = i
	}
	indegree := make([]int, len(r.Services))
	dependents := make([][]int, len(r.Services))
	for i, s := range r.Services {
		for _, d := range s.DependsOn {
			j, known := index[d]
			if !known {
				continue
			}
			indegree[i]++
			dependents[j] = append(dependents[j], i)
		}
	}

	done := make([]bool, len(r.Services))
	for len(names) < len(r.Services) {
		// Lowest render index among the ready ones.
		next := -1
		for i := range r.Services {
			if !done[i] && indegree[i] == 0 {
				next = i
				break
			}
		}
		if next < 0 {
			return r.renderOrder(), false
		}
		done[next] = true
		names = append(names, r.Services[next].Name)
		for _, k := range dependents[next] {
			indegree[k]--
		}
	}
	return names, true
}

func (r *Rendered) renderOrder() []string {
	out := make([]string, len(r.Services))
	for i, s := range r.Services {
		out[i] = s.Name
	}
	return out
}

var pinVarRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)`)

// PinVar returns the first ${VAR} named in the service's image: in the raw
// compose file — the variable its pin lives in.
func PinVar(rawCompose []byte, service string) (string, error) {
	var raw struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(rawCompose, &raw); err != nil {
		return "", fmt.Errorf("parse compose.yaml: %w", err)
	}
	image := raw.Services[service].Image
	m := pinVarRE.FindStringSubmatch(image)
	if m == nil {
		return "", fmt.Errorf("cannot detect pin variable in raw compose image: %s", image)
	}
	return m[1], nil
}
