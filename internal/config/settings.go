// Package config resolves nexr settings from flags, environment variables and
// the YAML config file, following docs/specification.md §5.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Settings is one layer of configuration. Nil values are unset.
type Settings struct {
	URL             *string        `yaml:"url"`
	User            *string        `yaml:"user"`
	Password        *string        `yaml:"password"`
	PasswordEnv     *string        `yaml:"password_env"`
	PasswordFile    *string        `yaml:"password_file"`
	PasswordCommand *string        `yaml:"password_command"`
	TLS             TLSSettings    `yaml:"tls"`
	Timeout         *Duration      `yaml:"timeout"`
	Retries         *int           `yaml:"retries"`
	Concurrency     *int           `yaml:"concurrency"`
	Output          *string        `yaml:"output"`
	Docker          DockerSettings `yaml:"docker"`
	Upload          UploadSettings `yaml:"upload"`
	GC              GCSettings     `yaml:"gc"`
}

// TLSSettings holds TLS options.
type TLSSettings struct {
	Insecure   *bool   `yaml:"insecure"`
	CAFile     *string `yaml:"ca_file"`
	ClientCert *string `yaml:"client_cert"`
	ClientKey  *string `yaml:"client_key"`
}

// DockerSettings holds options of the docker commands.
type DockerSettings struct {
	Repository   *string           `yaml:"repository"`
	Exclude      *[]string         `yaml:"exclude"`
	RegistryURLs map[string]string `yaml:"registry_urls"`
}

// UploadSettings holds options of the upload command.
type UploadSettings struct {
	Method *string `yaml:"method"`
}

// GCSettings holds options of the gc command.
type GCSettings struct {
	WaitTimeout *Duration `yaml:"wait_timeout"`
	Tasks       *[]string `yaml:"tasks"`
}

// File is the structure of the YAML config file. Top-level settings are
// defaults inherited by every profile.
type File struct {
	CurrentProfile string `yaml:"current_profile"`
	Settings       `yaml:",inline"`
	Profiles       map[string]Settings `yaml:"profiles"`
}

// Duration is a time.Duration that also accepts days ("30d") and weeks ("2w").
type Duration struct {
	time.Duration
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: duration must be a string such as \"60s\" or \"30d\"", n.Line)
	}
	v, err := ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	d.Duration = v
	return nil
}

// ParseDuration parses Go duration syntax plus whole days ("30d") and weeks ("2w").
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if n := len(s); n > 1 && (s[n-1] == 'd' || s[n-1] == 'w') {
		if v, err := strconv.Atoi(s[:n-1]); err == nil && v >= 0 {
			unit := 24 * time.Hour
			if s[n-1] == 'w' {
				unit *= 7
			}
			return time.Duration(v) * unit, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (examples: 90s, 36h, 30d, 2w)", s)
	}
	return d, nil
}

// passwordKeys returns the password sources set in the layer, as
// (kind, value) pairs.
func (s Settings) passwordKeys() [][2]string {
	var out [][2]string
	if s.Password != nil {
		out = append(out, [2]string{"password", *s.Password})
	}
	if s.PasswordEnv != nil {
		out = append(out, [2]string{"password_env", *s.PasswordEnv})
	}
	if s.PasswordFile != nil {
		out = append(out, [2]string{"password_file", *s.PasswordFile})
	}
	if s.PasswordCommand != nil {
		out = append(out, [2]string{"password_command", *s.PasswordCommand})
	}
	return out
}
