// Package config loads and validates the side-effect-free session document.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	gitdetect "github.com/webportdev/webport/cmd/webportctl/git"
	"gopkg.in/yaml.v3"
)

const Version = 1

type Options struct {
	CurrentDir   string
	ConfigPath   string
	WorktreeRoot string
}

type Source struct {
	PrimaryPath string
	LocalPath   string
}

type Config struct {
	Version      int                `yaml:"version"`
	Project      string             `yaml:"project"`
	Branch       string             `yaml:"branch"`
	WorktreeRoot string             `yaml:"worktree_root"`
	Session      Session            `yaml:"session"`
	Requires     Requires           `yaml:"requires"`
	Ports        map[string]Port    `yaml:"ports"`
	Env          map[string]Value   `yaml:"env"`
	Services     map[string]Service `yaml:"services"`
	Profiles     map[string]Profile `yaml:"profiles"`
	Source       Source             `yaml:"-"`
}

type Session struct {
	EnvFiles          []string          `yaml:"env_files"`
	RetainLastSummary *bool             `yaml:"retain_last_summary"`
	Exports           map[string]string `yaml:"exports"`
}

type Requires struct {
	Commands []string `yaml:"commands"`
}

type Port struct {
	Fixed     *int  `yaml:"fixed"`
	FirstFree *int  `yaml:"first_free"`
	Random    []int `yaml:"random"`
	Discover  bool  `yaml:"discover"`
}

type Value struct {
	Literal   *string   `yaml:"value,omitempty"`
	Sensitive bool      `yaml:"sensitive,omitempty"`
	Generate  *Generate `yaml:"generate,omitempty"`
}

func (v *Value) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if node.Tag == "!!null" {
			return errors.New("null is not allowed")
		}
		value := node.Value
		v.Literal = &value
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("must be a scalar or mapping")
	}
	type valueAlias Value
	var decoded valueAlias
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*v = Value(decoded)
	return nil
}

type Generate struct {
	Lifetime string   `yaml:"lifetime"`
	Length   int      `yaml:"length"`
	Sets     []string `yaml:"sets"`
	Alphabet string   `yaml:"alphabet"`
}

type Service struct {
	Command    []string          `yaml:"command"`
	Shell      string            `yaml:"shell"`
	WorkingDir string            `yaml:"working_dir"`
	Env        map[string]Value  `yaml:"env"`
	DependsOn  []string          `yaml:"depends_on"`
	Completion string            `yaml:"completion"`
	Ready      *Ready            `yaml:"ready"`
	Endpoints  map[string]string `yaml:"endpoints"`
	Logs       *Logs             `yaml:"logs"`
	Shutdown   *Shutdown         `yaml:"shutdown"`
	Route      *Route            `yaml:"route"`
	Platform   []string          `yaml:"platform"`
}

type Ready struct {
	TCP     string     `yaml:"tcp"`
	HTTP    *HTTPReady `yaml:"http"`
	Command []string   `yaml:"command"`
}

type HTTPReady struct {
	URL                string            `yaml:"url"`
	Method             string            `yaml:"method"`
	Status             []int             `yaml:"status"`
	Headers            map[string]string `yaml:"headers"`
	Interval           Duration          `yaml:"interval"`
	Timeout            Duration          `yaml:"timeout"`
	OverallTimeout     Duration          `yaml:"overall_timeout"`
	InsecureSkipVerify bool              `yaml:"insecure_skip_verify"`
}

type Logs struct {
	Destination string `yaml:"destination"`
	Path        string `yaml:"path"`
	Mode        string `yaml:"mode"`
	Streams     string `yaml:"streams"`
	MaxBytes    int64  `yaml:"max_bytes"`
	Backups     int    `yaml:"backups"`
}

type Shutdown struct {
	Signal      string   `yaml:"signal"`
	GracePeriod Duration `yaml:"grace_period"`
	Command     []string `yaml:"command"`
	Timeout     Duration `yaml:"timeout"`
}

type Route struct {
	Project  string            `yaml:"project"`
	Branch   string            `yaml:"branch"`
	Port     string            `yaml:"port"`
	Optional bool              `yaml:"optional"`
	Export   map[string]string `yaml:"export"`
}

type Profile struct {
	Services []string         `yaml:"services"`
	Env      map[string]Value `yaml:"env"`
}

type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return errors.New("duration must be a string")
	}
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Duration() time.Duration { return time.Duration(d) }

type Error struct {
	File   string
	Path   string
	Line   int
	Column int
	Err    error
}

func (e *Error) Error() string {
	location := e.File
	if e.Line > 0 {
		location += fmt.Sprintf(":%d:%d", e.Line, e.Column)
	}
	if e.Path != "" {
		location += ": " + e.Path
	}
	return fmt.Sprintf("%s: %v", location, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

func Load(opts Options) (Config, error) {
	primary, err := locatePrimary(opts)
	if err != nil {
		return Config{}, err
	}
	local := filepath.Join(filepath.Dir(primary), ".webport.local.yaml")

	primaryNode, err := readDocument(primary)
	if err != nil {
		return Config{}, err
	}
	if err := validateDocument(primaryNode, primary, true); err != nil {
		return Config{}, err
	}

	merged := primaryNode
	if _, statErr := os.Stat(local); statErr == nil {
		localNode, readErr := readDocument(local)
		if readErr != nil {
			return Config{}, readErr
		}
		if err := validateDocument(localNode, local, false); err != nil {
			return Config{}, err
		}
		merged = mergeNodes(primaryNode, localNode)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Config{}, sourceError(local, nil, "", statErr)
	}
	if err := validateDocument(merged, primary, true); err != nil {
		return Config{}, err
	}

	var result Config
	if err := merged.Decode(&result); err != nil {
		return Config{}, decodeError(primary, merged, err)
	}
	if result.Version != Version {
		return Config{}, sourceError(primary, merged, "version", fmt.Errorf("unsupported version %d; want %d", result.Version, Version))
	}
	result.Source = Source{PrimaryPath: primary}
	if _, err := os.Stat(local); err == nil {
		result.Source.LocalPath = local
	}
	if result.Session.RetainLastSummary == nil {
		keep := true
		result.Session.RetainLastSummary = &keep
	}
	return result, nil
}

func LoadFrom(path string) (Config, error) { return Load(Options{ConfigPath: path}) }

func locatePrimary(opts Options) (string, error) {
	if opts.ConfigPath != "" {
		path, err := filepath.Abs(opts.ConfigPath)
		if err != nil {
			return "", fmt.Errorf("resolve config path: %w", err)
		}
		if info, statErr := os.Stat(path); statErr != nil {
			return "", sourceError(path, nil, "", statErr)
		} else if info.IsDir() {
			return "", sourceError(path, nil, "", errors.New("config path is a directory"))
		}
		return filepath.Clean(path), nil
	}
	start := opts.CurrentDir
	if start == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
	}
	start, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve current directory: %w", err)
	}
	if opts.WorktreeRoot == "" {
		root, rootErr := gitdetect.FindWorktreeRoot(start)
		if rootErr != nil {
			return "", fmt.Errorf("discover .webport.yaml: %w; use --config PATH", rootErr)
		}
		opts.WorktreeRoot = root
	}
	root, err := filepath.Abs(opts.WorktreeRoot)
	if err != nil {
		return "", fmt.Errorf("resolve worktree root: %w", err)
	}
	for current := start; ; current = filepath.Dir(current) {
		candidate := filepath.Join(current, ".webport.yaml")
		if info, statErr := os.Stat(candidate); statErr == nil {
			if info.IsDir() {
				return "", sourceError(candidate, nil, "", errors.New("config path is a directory"))
			}
			return filepath.Clean(candidate), nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", sourceError(candidate, nil, "", statErr)
		}
		if current == root {
			break
		}
		parent := filepath.Dir(current)
		if parent == current || !withinRoot(parent, root) {
			break
		}
	}
	return "", sourceError(filepath.Join(start, ".webport.yaml"), nil, "", errors.New("file not found before worktree root"))
}

func withinRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func readDocument(path string) (*yaml.Node, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, sourceError(path, nil, "", err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, decodeError(path, &document, err)
	}
	markSource(&document, path)
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, sourceError(path, &extra, "", errors.New("multiple YAML documents are not supported"))
		}
		return nil, decodeError(path, &extra, err)
	}
	return &document, nil
}

func validateDocument(document *yaml.Node, file string, requireVersion bool) error {
	node := unwrap(document)
	if node == nil || node.Kind != yaml.MappingNode {
		return sourceError(file, node, "", errors.New("document must be a mapping"))
	}
	if err := validateMapping(node, file, "", map[string]func(*yaml.Node, string) error{
		"version":       scalarType(yaml.ScalarNode, "integer"),
		"project":       scalarType(yaml.ScalarNode, "string"),
		"branch":        scalarType(yaml.ScalarNode, "string"),
		"worktree_root": scalarType(yaml.ScalarNode, "string"),
		"session":       validateSession,
		"requires":      validateRequires,
		"ports":         validatePorts,
		"env":           validateEnv,
		"services":      validateServices,
		"profiles":      validateProfiles,
	}); err != nil {
		return err
	}
	version, ok := lookup(node, "version")
	if !ok && requireVersion {
		return sourceError(file, node, "version", errors.New("required field is missing"))
	}
	if ok && version.Value != "1" {
		return sourceError(file, version, "version", fmt.Errorf("unsupported version %q; want 1", version.Value))
	}
	return nil
}

func validateSession(node *yaml.Node, path string) error {
	return validateMapping(node, currentFile(node), path, map[string]func(*yaml.Node, string) error{
		"env_files":           sequenceOf("string"),
		"retain_last_summary": scalarType(yaml.ScalarNode, "boolean"),
		"exports":             func(n *yaml.Node, p string) error { return mapOfScalars(n, p, "string") },
	})
}

func validateRequires(node *yaml.Node, path string) error {
	return validateMapping(node, currentFile(node), path, map[string]func(*yaml.Node, string) error{
		"commands": sequenceOf("string"),
	})
}

func validatePorts(node *yaml.Node, path string) error {
	return validateDynamicMap(node, currentFile(node), path, func(n *yaml.Node, p string) error {
		return validateMapping(n, currentFile(n), p, map[string]func(*yaml.Node, string) error{
			"fixed":      scalarType(yaml.ScalarNode, "integer"),
			"first_free": scalarType(yaml.ScalarNode, "integer"),
			"random":     sequenceOf("integer"),
			"discover":   scalarType(yaml.ScalarNode, "boolean"),
		})
	})
}

func validateEnv(node *yaml.Node, path string) error {
	return validateDynamicMap(node, currentFile(node), path, validateValue)
}

func validateValue(node *yaml.Node, path string) error {
	if node.Kind == yaml.ScalarNode {
		if node.Tag == "!!null" {
			return sourceError(currentFile(node), node, path, errors.New("null is not allowed"))
		}
		return nil
	}
	return validateMapping(node, currentFile(node), path, map[string]func(*yaml.Node, string) error{
		"value":     scalarType(yaml.ScalarNode, "scalar"),
		"sensitive": scalarType(yaml.ScalarNode, "boolean"),
		"generate":  validateGenerate,
	})
}

func validateGenerate(node *yaml.Node, path string) error {
	return validateMapping(node, currentFile(node), path, map[string]func(*yaml.Node, string) error{
		"lifetime": scalarType(yaml.ScalarNode, "string"),
		"length":   scalarType(yaml.ScalarNode, "integer"),
		"sets":     sequenceOf("string"),
		"alphabet": scalarType(yaml.ScalarNode, "string"),
	})
}

func validateServices(node *yaml.Node, path string) error {
	return validateDynamicMap(node, currentFile(node), path, validateService)
}

func validateService(node *yaml.Node, path string) error {
	return validateMapping(node, currentFile(node), path, map[string]func(*yaml.Node, string) error{
		"command":     sequenceOf("string"),
		"shell":       scalarType(yaml.ScalarNode, "string"),
		"working_dir": scalarType(yaml.ScalarNode, "string"),
		"env":         validateEnv,
		"depends_on":  sequenceOf("string"),
		"completion":  scalarType(yaml.ScalarNode, "string"),
		"ready":       validateReady,
		"endpoints":   func(n *yaml.Node, p string) error { return mapOfScalars(n, p, "string") },
		"logs":        validateLogs,
		"shutdown":    validateShutdown,
		"route":       validateRoute,
		"platform":    sequenceOf("string"),
	})
}

func validateReady(node *yaml.Node, path string) error {
	return validateMapping(node, currentFile(node), path, map[string]func(*yaml.Node, string) error{
		"tcp":     scalarType(yaml.ScalarNode, "string"),
		"http":    validateHTTP,
		"command": sequenceOf("string"),
	})
}

func validateHTTP(node *yaml.Node, path string) error {
	return validateMapping(node, currentFile(node), path, map[string]func(*yaml.Node, string) error{
		"url":                  scalarType(yaml.ScalarNode, "string"),
		"method":               scalarType(yaml.ScalarNode, "string"),
		"status":               sequenceOf("integer"),
		"headers":              func(n *yaml.Node, p string) error { return mapOfScalars(n, p, "string") },
		"interval":             durationValue,
		"timeout":              durationValue,
		"overall_timeout":      durationValue,
		"insecure_skip_verify": scalarType(yaml.ScalarNode, "boolean"),
	})
}

func validateLogs(node *yaml.Node, path string) error {
	return validateMapping(node, currentFile(node), path, map[string]func(*yaml.Node, string) error{
		"destination": scalarType(yaml.ScalarNode, "string"),
		"path":        scalarType(yaml.ScalarNode, "string"),
		"mode":        scalarType(yaml.ScalarNode, "string"),
		"streams":     scalarType(yaml.ScalarNode, "string"),
		"max_bytes":   scalarType(yaml.ScalarNode, "integer"),
		"backups":     scalarType(yaml.ScalarNode, "integer"),
	})
}

func validateShutdown(node *yaml.Node, path string) error {
	return validateMapping(node, currentFile(node), path, map[string]func(*yaml.Node, string) error{
		"signal":       scalarType(yaml.ScalarNode, "string"),
		"grace_period": durationValue,
		"command":      sequenceOf("string"),
		"timeout":      durationValue,
	})
}

func validateRoute(node *yaml.Node, path string) error {
	return validateMapping(node, currentFile(node), path, map[string]func(*yaml.Node, string) error{
		"project":  scalarType(yaml.ScalarNode, "string"),
		"branch":   scalarType(yaml.ScalarNode, "string"),
		"port":     scalarType(yaml.ScalarNode, "scalar"),
		"optional": scalarType(yaml.ScalarNode, "boolean"),
		"export":   validateExport,
	})
}

func validateExport(node *yaml.Node, path string) error {
	return validateMapping(node, currentFile(node), path, map[string]func(*yaml.Node, string) error{
		"host": scalarType(yaml.ScalarNode, "string"),
		"url":  scalarType(yaml.ScalarNode, "string"),
	})
}

func validateProfiles(node *yaml.Node, path string) error {
	return validateDynamicMap(node, currentFile(node), path, func(n *yaml.Node, p string) error {
		return validateMapping(n, currentFile(n), p, map[string]func(*yaml.Node, string) error{
			"services": sequenceOf("string"),
			"env":      validateEnv,
		})
	})
}

func durationValue(node *yaml.Node, path string) error {
	if err := scalarType(yaml.ScalarNode, "string")(node, path); err != nil {
		return err
	}
	if _, err := time.ParseDuration(node.Value); err != nil {
		return sourceError(currentFile(node), node, path, fmt.Errorf("invalid duration %q: %w", node.Value, err))
	}
	return nil
}

func validateMapping(node *yaml.Node, file, path string, fields map[string]func(*yaml.Node, string) error) error {
	if node.Kind != yaml.MappingNode {
		return sourceError(file, node, path, errors.New("must be a mapping"))
	}
	seen := make(map[string]struct{})
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		name := key.Value
		if _, exists := seen[name]; exists {
			return sourceError(file, key, joinPath(path, name), errors.New("duplicate key"))
		}
		seen[name] = struct{}{}
		check, ok := fields[name]
		if !ok {
			return sourceError(file, key, joinPath(path, name), errors.New("unknown field"))
		}
		if err := check(value, joinPath(path, name)); err != nil {
			return err
		}
	}
	return nil
}

func validateDynamicMap(node *yaml.Node, file, path string, check func(*yaml.Node, string) error) error {
	if node.Kind != yaml.MappingNode {
		return sourceError(file, node, path, errors.New("must be a mapping"))
	}
	seen := make(map[string]struct{})
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if _, exists := seen[key.Value]; exists {
			return sourceError(file, key, joinPath(path, key.Value), errors.New("duplicate key"))
		}
		seen[key.Value] = struct{}{}
		if key.Value == "" {
			return sourceError(file, key, path, errors.New("name must not be empty"))
		}
		if err := check(value, joinPath(path, key.Value)); err != nil {
			return err
		}
	}
	return nil
}

func sequenceOf(kind string) func(*yaml.Node, string) error {
	return func(node *yaml.Node, path string) error {
		if node.Kind != yaml.SequenceNode {
			return sourceError(currentFile(node), node, path, errors.New("must be a sequence"))
		}
		for _, item := range node.Content {
			if err := scalarType(yaml.ScalarNode, kind)(item, path); err != nil {
				return err
			}
		}
		return nil
	}
}

func mapOfScalars(node *yaml.Node, path, kind string) error {
	return validateDynamicMap(node, currentFile(node), path, func(value *yaml.Node, valuePath string) error {
		return scalarType(yaml.ScalarNode, kind)(value, valuePath)
	})
}

func scalarType(kind yaml.Kind, expected string) func(*yaml.Node, string) error {
	return func(node *yaml.Node, path string) error {
		if node.Kind != kind || node.Tag == "!!null" {
			return sourceError(currentFile(node), node, path, fmt.Errorf("must be a %s", expected))
		}
		switch expected {
		case "integer":
			if node.Tag != "!!int" {
				return sourceError(currentFile(node), node, path, errors.New("must be an integer"))
			}
		case "boolean":
			if node.Tag != "!!bool" {
				return sourceError(currentFile(node), node, path, errors.New("must be a boolean"))
			}
		case "string":
			if node.Tag != "!!str" {
				return sourceError(currentFile(node), node, path, errors.New("must be a string"))
			}
		case "scalar":
			if node.Tag != "!!str" && node.Tag != "!!int" && node.Tag != "!!bool" {
				return sourceError(currentFile(node), node, path, errors.New("must be a scalar"))
			}
		}
		return nil
	}
}

func mergeNodes(base, overlay *yaml.Node) *yaml.Node {
	base = unwrap(base)
	overlay = unwrap(overlay)
	if base.Kind != yaml.MappingNode || overlay.Kind != yaml.MappingNode {
		return overlay
	}
	result := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Line: overlay.Line, Column: overlay.Column}
	for i := 0; i < len(base.Content); i += 2 {
		result.Content = append(result.Content, base.Content[i], base.Content[i+1])
	}
	for i := 0; i < len(overlay.Content); i += 2 {
		key, value := overlay.Content[i], overlay.Content[i+1]
		if index := mappingIndex(result, key.Value); index >= 0 {
			result.Content[index+1] = mergeNodes(result.Content[index+1], value)
		} else {
			result.Content = append(result.Content, key, value)
		}
	}
	return result
}

func mappingIndex(node *yaml.Node, key string) int {
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func lookup(node *yaml.Node, key string) (*yaml.Node, bool) {
	if index := mappingIndex(node, key); index >= 0 {
		return node.Content[index+1], true
	}
	return nil, false
}

func unwrap(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		node = node.Content[0]
	}
	return node
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// YAML nodes do not carry their source filename. Validation receives the
// filename at the public boundary; this fallback keeps nested callbacks
// useful while preserving line and column information in the returned error.
var nodeSources sync.Map

func markSource(node *yaml.Node, file string) {
	if node == nil {
		return
	}
	nodeSources.Store(node, file)
	for _, child := range node.Content {
		markSource(child, file)
	}
}

func currentFile(node *yaml.Node) string {
	if file, ok := nodeSources.Load(node); ok {
		return file.(string)
	}
	return "configuration"
}

func sourceError(file string, node *yaml.Node, path string, err error) error {
	if file == "" {
		file = "configuration"
	}
	result := &Error{File: file, Path: path, Err: err}
	if node != nil {
		result.Line, result.Column = node.Line, node.Column
	}
	return result
}

func decodeError(file string, node *yaml.Node, err error) error {
	return sourceError(file, node, "", err)
}
