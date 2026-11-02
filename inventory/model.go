// Package inventory defines inventory data and deterministic resolution rules.
// It does not read files or contact managed hosts.
package inventory

type Inventory struct {
	Tags          map[string]Tag               `json:"tags" yaml:"tags"`
	Services      map[string]ServiceDefinition `json:"services" yaml:"services"`
	Hosts         []Host                       `json:"hosts" yaml:"hosts"`
	SecretSources map[string]SecretSource      `json:"secret_sources,omitempty" yaml:"secret_sources"`
}

type Tag struct {
	Order    int                          `json:"order" yaml:"order"`
	Services map[string]ServiceAssignment `json:"services" yaml:"services"`
	Vars     map[string]any               `json:"vars" yaml:"vars"`
}

type Host struct {
	Name      string    `json:"name" yaml:"name"`
	Tags      []string  `json:"tags" yaml:"tags"`
	Transport Transport `json:"transport" yaml:"transport"`
}

// ServiceAssignment declares a service's target state through a tag.
type ServiceAssignment struct {
	State string `json:"state" yaml:"state"`
}

// ServiceDefinition keeps required inventory ordering outside bare components.
type ServiceDefinition struct {
	Order   *int `json:"order" yaml:"order"`
	Service `yaml:",inline"`
}

type Transport struct {
	Type string `json:"type" yaml:"type"`
	Host string `json:"host,omitempty" yaml:"host"`
	User string `json:"user,omitempty" yaml:"user"`
	Port int    `json:"port,omitempty" yaml:"port"`
	Sudo bool   `json:"sudo,omitempty" yaml:"sudo"`
}

type Assignment struct {
	Name  string `json:"name" yaml:"name"`
	Order int    `json:"order" yaml:"order"`
	State string `json:"state" yaml:"state"`
}

type Service struct {
	Binaries   map[string]Binary  `json:"binaries,omitempty" yaml:"binaries"`
	Configs    map[string]Config  `json:"configs,omitempty" yaml:"configs"`
	Processes  map[string]Process `json:"processes,omitempty" yaml:"processes"`
	SourceFile string             `json:"source_file,omitempty" yaml:"-"`
}

type Binary struct {
	Type    string `json:"type" yaml:"type"`
	Manager string `json:"manager,omitempty" yaml:"manager"`
	Package string `json:"package" yaml:"package"`
	Version string `json:"version,omitempty" yaml:"version"`
}

type Template struct {
	File   string  `json:"file,omitempty" yaml:"file"`
	Inline *string `json:"inline,omitempty" yaml:"inline"`
}

type Config struct {
	Path      string   `json:"path" yaml:"path"`
	Owner     string   `json:"owner,omitempty" yaml:"owner"`
	Group     string   `json:"group,omitempty" yaml:"group"`
	Mode      string   `json:"mode,omitempty" yaml:"mode"`
	Template  Template `json:"template" yaml:"template"`
	Sensitive bool     `json:"sensitive,omitempty" yaml:"sensitive"`
	SELinux   string   `json:"selinux,omitempty" yaml:"selinux"`
}

type Process struct {
	Type      string            `json:"type" yaml:"type"`
	Unit      string            `json:"unit" yaml:"unit"`
	State     string            `json:"state" yaml:"state"`
	Enabled   *bool             `json:"enabled,omitempty" yaml:"enabled"`
	Timeout   string            `json:"timeout" yaml:"timeout"`
	RestartOn []string          `json:"restart_on,omitempty" yaml:"restart_on"`
	ReloadOn  []string          `json:"reload_on,omitempty" yaml:"reload_on"`
	After     []string          `json:"after,omitempty" yaml:"after"`
	UnitFile  *Config           `json:"unit_file,omitempty" yaml:"unit_file"`
	Overrides map[string]Config `json:"overrides,omitempty" yaml:"overrides"`
}

// SecretSource is a declaration only. Inventory resolution never reads its file.
type SecretSource struct {
	Type string `json:"type" yaml:"type"`
	File string `json:"file" yaml:"file"`
}

type Selectors struct{ Hosts, Tags, Services []string }

type Resolved struct {
	Hosts         []ResolvedHost          `json:"hosts"`
	SecretSources map[string]SecretSource `json:"secret_sources,omitempty"`
}

type ResolvedHost struct {
	Name      string            `json:"name"`
	Tags      []string          `json:"tags"`
	Transport Transport         `json:"transport"`
	Vars      map[string]any    `json:"vars"`
	Services  []ResolvedService `json:"services"`
}

type ResolvedService struct {
	Assignment
	Definition Service `json:"definition"`
}
