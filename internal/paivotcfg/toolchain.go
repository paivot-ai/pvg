package paivotcfg

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// WriteToolchain writes (or replaces) the toolchain pin block in
// projectRoot/.paivot/config.yaml. The rest of the file -- including
// comments from `pvg init` scaffolding -- is preserved by editing the YAML
// document tree instead of re-marshalling a struct. A missing config file
// is created with only the toolchain block.
func WriteToolchain(projectRoot string, tc Toolchain) error {
	if projectRoot == "" {
		return errors.New("paivotcfg.WriteToolchain: projectRoot is empty")
	}

	dir := filepath.Join(projectRoot, ConfigDir)
	path := filepath.Join(dir, ConfigFile)

	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	var doc yaml.Node
	if len(data) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		doc = yaml.Node{
			Kind:    yaml.DocumentNode,
			Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}},
		}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: top-level YAML is not a mapping", path)
	}

	tcNode := &yaml.Node{}
	if err := tcNode.Encode(tc); err != nil {
		return fmt.Errorf("encode toolchain: %w", err)
	}

	replaced := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "toolchain" {
			root.Content[i+1] = tcNode
			replaced = true
			break
		}
	}
	if !replaced {
		key := &yaml.Node{
			Kind:        yaml.ScalarNode,
			Tag:         "!!str",
			Value:       "toolchain",
			HeadComment: "Toolchain pin written by `pvg update --pin`. The session-start hook\nwarns when installed versions drift from these pins.",
		}
		root.Content = append(root.Content, key, tcNode)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// LoadToolchain returns the toolchain pin for the project, or nil when no
// config file or no toolchain block exists. Unlike Load, it never fails on
// adapter-section validation -- the pin check must not depend on backlog
// configuration being valid.
func LoadToolchain(projectRoot string) (*Toolchain, error) {
	if projectRoot == "" {
		return nil, errors.New("paivotcfg.LoadToolchain: projectRoot is empty")
	}
	path := filepath.Join(projectRoot, ConfigDir, ConfigFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg struct {
		Toolchain *Toolchain `yaml:"toolchain"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg.Toolchain, nil
}
