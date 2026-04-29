// Package paivotcfg loads the per-repo .paivot/config.yaml that selects which
// backlog and notes adapters pvg routes through.
//
// Missing file is not an error: callers receive a Config populated with the
// historical defaults (nd backlog + vlt notes, no mirrors), so existing repos
// keep working unchanged.
package paivotcfg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

const (
	// ConfigDir is the per-repo directory holding paivot config.
	ConfigDir = ".paivot"
	// ConfigFile is the YAML config file inside ConfigDir.
	ConfigFile = "config.yaml"

	DefaultBacklogAdapter = "nd"
	DefaultNotesAdapter   = "vlt"

	defaultNdVault  = ".vault"
	defaultVltVault = "Claude"
)

// Config is the loaded shape of .paivot/config.yaml.
type Config struct {
	Backlog Section `yaml:"backlog"`
	Notes   Section `yaml:"notes"`
}

// Section configures one provider boundary (backlog or notes) with a primary
// adapter and zero-or-more best-effort mirror adapters.
type Section struct {
	Primary AdapterRef   `yaml:"primary"`
	Mirrors []AdapterRef `yaml:"mirrors,omitempty"`
}

// AdapterRef names an adapter and supplies its config bag.
type AdapterRef struct {
	Adapter string                 `yaml:"adapter"`
	Config  map[string]interface{} `yaml:"config,omitempty"`
}

// Defaults returns the zero-disruption configuration: nd backlog + vlt notes
// with no mirrors. Used when no .paivot/config.yaml is present.
func Defaults() Config {
	return Config{
		Backlog: Section{
			Primary: AdapterRef{
				Adapter: DefaultBacklogAdapter,
				Config:  map[string]interface{}{"vault": defaultNdVault},
			},
		},
		Notes: Section{
			Primary: AdapterRef{
				Adapter: DefaultNotesAdapter,
				Config:  map[string]interface{}{"vault": defaultVltVault},
			},
		},
	}
}

// Load returns the config for the given project root. If
// projectRoot/.paivot/config.yaml does not exist, Defaults() is returned.
// Env-var interpolation is applied eagerly: any string value of the form
// ${NAME} or $NAME is replaced with os.Getenv("NAME") before validation.
func Load(projectRoot string) (Config, error) {
	if projectRoot == "" {
		return Config{}, errors.New("paivotcfg.Load: projectRoot is empty")
	}

	path := filepath.Join(projectRoot, ConfigDir, ConfigFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}

	applyDefaults(&cfg)
	interpolateEnv(&cfg)

	if err := validate(&cfg); err != nil {
		return Config{}, fmt.Errorf("validate %s: %w", path, err)
	}
	return cfg, nil
}

// LocateProjectRoot walks up from start looking for a .paivot directory or a
// .git directory. The first ancestor containing either is returned.
func LocateProjectRoot(start string) (string, error) {
	if start == "" {
		return "", errors.New("paivotcfg.LocateProjectRoot: start is empty")
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if isDir(filepath.Join(dir, ConfigDir)) || isDir(filepath.Join(dir, ".git")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no project root found above %s", start)
		}
		dir = parent
	}
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func applyDefaults(cfg *Config) {
	d := Defaults()
	if cfg.Backlog.Primary.Adapter == "" {
		cfg.Backlog.Primary = d.Backlog.Primary
	}
	if cfg.Notes.Primary.Adapter == "" {
		cfg.Notes.Primary = d.Notes.Primary
	}
}

func validate(cfg *Config) error {
	if cfg.Backlog.Primary.Adapter == "" {
		return errors.New("backlog.primary.adapter is required")
	}
	if cfg.Notes.Primary.Adapter == "" {
		return errors.New("notes.primary.adapter is required")
	}
	for i, m := range cfg.Backlog.Mirrors {
		if m.Adapter == "" {
			return fmt.Errorf("backlog.mirrors[%d].adapter is required", i)
		}
	}
	for i, m := range cfg.Notes.Mirrors {
		if m.Adapter == "" {
			return fmt.Errorf("notes.mirrors[%d].adapter is required", i)
		}
	}
	return nil
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

func interpolateEnv(cfg *Config) {
	interpolateRef(&cfg.Backlog.Primary)
	for i := range cfg.Backlog.Mirrors {
		interpolateRef(&cfg.Backlog.Mirrors[i])
	}
	interpolateRef(&cfg.Notes.Primary)
	for i := range cfg.Notes.Mirrors {
		interpolateRef(&cfg.Notes.Mirrors[i])
	}
}

func interpolateRef(r *AdapterRef) {
	for k, v := range r.Config {
		r.Config[k] = interpolateValue(v)
	}
}

func interpolateValue(v interface{}) interface{} {
	switch x := v.(type) {
	case string:
		return envPattern.ReplaceAllStringFunc(x, func(match string) string {
			m := envPattern.FindStringSubmatch(match)
			name := m[1]
			if name == "" {
				name = m[2]
			}
			return os.Getenv(name)
		})
	case map[string]interface{}:
		for k, vv := range x {
			x[k] = interpolateValue(vv)
		}
		return x
	case []interface{}:
		for i := range x {
			x[i] = interpolateValue(x[i])
		}
		return x
	}
	return v
}
