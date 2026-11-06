// Package inventory defines inventory data and deterministic resolution rules.
// It does not read files or contact managed hosts.
package inventory

type Inventory struct {
	// SourceIdentity is the configured root document location, retained only for
	// controller-side derived data. It is not inventory syntax or plan input.
	SourceIdentity string                       `json:"-" yaml:"-"`
	Tags           map[string]Tag               `json:"tags" yaml:"tags"`
	Services       map[string]ServiceDefinition `json:"services" yaml:"services"`
	Hosts          []Host                       `json:"hosts" yaml:"hosts"`
	SecretSources  map[string]SecretSource      `json:"secret_sources,omitempty" yaml:"secret_sources"`
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

// ServiceDefinition combines required execution ordering and service components.
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
	Binaries   []Binary  `json:"binaries,omitempty" yaml:"binaries"`
	Configs    []Config  `json:"configs,omitempty" yaml:"configs"`
	Processes  []Process `json:"processes,omitempty" yaml:"processes"`
	SourceFile string    `json:"source_file,omitempty" yaml:"-"`
	SourceURL  string    `json:"source_url,omitempty" yaml:"-"`
}

type Binary struct {
	Name            string `json:"name,omitempty" yaml:"name"`
	Type            string `json:"type" yaml:"type"`
	Manager         string `json:"manager,omitempty" yaml:"manager"`
	Package         string `json:"package,omitempty" yaml:"package"`
	Version         string `json:"version,omitempty" yaml:"version"`
	DisableExcludes string `json:"disable_excludes,omitempty" yaml:"disable_excludes"`
	URL             string `json:"url,omitempty" yaml:"url"`
	SHA256          string `json:"sha256,omitempty" yaml:"sha256"`
	Path            string `json:"path,omitempty" yaml:"path"`
	Owner           string `json:"owner,omitempty" yaml:"owner"`
	Group           string `json:"group,omitempty" yaml:"group"`
	Mode            string `json:"mode,omitempty" yaml:"mode"`
	Timeout         string `json:"timeout,omitempty" yaml:"timeout"`
	Format          string `json:"format,omitempty" yaml:"format"`
	StripComponents int    `json:"strip_components,omitempty" yaml:"strip_components"`
}

type Template struct {
	URL    string  `json:"url,omitempty" yaml:"url"`
	SHA256 string  `json:"sha256,omitempty" yaml:"sha256"`
	File   string  `json:"file,omitempty" yaml:"file"`
	Inline *string `json:"inline,omitempty" yaml:"inline"`
}

type Config struct {
	Name                string     `json:"name,omitempty" yaml:"name"`
	Type                string     `json:"type,omitempty" yaml:"type"`
	Path                string     `json:"path" yaml:"path"`
	Owner               string     `json:"owner,omitempty" yaml:"owner"`
	Group               string     `json:"group,omitempty" yaml:"group"`
	Mode                string     `json:"mode,omitempty" yaml:"mode"`
	Template            Template   `json:"template" yaml:"template"`
	Sensitive           bool       `json:"sensitive,omitempty" yaml:"sensitive"`
	AtomicWrites        *bool      `json:"atomic_writes,omitempty" yaml:"atomic_writes"`
	Backup              bool       `json:"backup,omitempty" yaml:"backup"`
	SELinux             string     `json:"selinux,omitempty" yaml:"selinux"`
	Validate            *Validator `json:"validate,omitempty" yaml:"validate"`
	Array               string     `json:"array,omitempty" yaml:"array"`
	Level               string     `json:"level,omitempty" yaml:"level"`
	Metadata            string     `json:"metadata,omitempty" yaml:"metadata"`
	Members             []string   `json:"members,omitempty" yaml:"members"`
	VG                  string     `json:"vg,omitempty" yaml:"vg"`
	Devices             []string   `json:"devices,omitempty" yaml:"devices"`
	LV                  string     `json:"lv,omitempty" yaml:"lv"`
	Size                string     `json:"size,omitempty" yaml:"size"`
	Filesystem          string     `json:"filesystem,omitempty" yaml:"filesystem"`
	Source              string     `json:"source,omitempty" yaml:"source"`
	Target              string     `json:"target,omitempty" yaml:"target"`
	Options             []string   `json:"options,omitempty" yaml:"options"`
	User                string     `json:"user,omitempty" yaml:"user"`
	UID                 *uint32    `json:"uid,omitempty" yaml:"uid"`
	GID                 *uint32    `json:"gid,omitempty" yaml:"gid"`
	System              *bool      `json:"system,omitempty" yaml:"system"`
	Home                string     `json:"home,omitempty" yaml:"home"`
	Shell               string     `json:"shell,omitempty" yaml:"shell"`
	Comment             *string    `json:"comment,omitempty" yaml:"comment"`
	SupplementaryGroups *[]string  `json:"supplementary_groups,omitempty" yaml:"supplementary_groups"`
	AppendGroups        *bool      `json:"append_groups,omitempty" yaml:"append_groups"`
}

// Validator checks a prepared candidate on the target before its content is written.
type Validator struct {
	Args    []string `json:"args" yaml:"args"`
	Timeout string   `json:"timeout,omitempty" yaml:"timeout"`
}

type Process struct {
	Name      string            `json:"name,omitempty" yaml:"name"`
	Type      string            `json:"type" yaml:"type"`
	Command   []string          `json:"command,omitempty" yaml:"command"`
	RunOn     []string          `json:"run_on,omitempty" yaml:"run_on"`
	Unit      string            `json:"unit,omitempty" yaml:"unit"`
	State     string            `json:"state,omitempty" yaml:"state"`
	Enabled   *bool             `json:"enabled,omitempty" yaml:"enabled"`
	Masked    *bool             `json:"masked,omitempty" yaml:"masked"`
	Timeout   string            `json:"timeout" yaml:"timeout"`
	RestartOn []string          `json:"restart_on,omitempty" yaml:"restart_on"`
	ReloadOn  []string          `json:"reload_on,omitempty" yaml:"reload_on"`
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
	SourceIdentity string                  `json:"-" yaml:"-"`
	Hosts          []ResolvedHost          `json:"hosts"`
	SecretSources  map[string]SecretSource `json:"secret_sources,omitempty"`
}

type ResolvedHost struct {
	Name      string            `json:"name"`
	Tags      []string          `json:"tags"`
	Transport Transport         `json:"transport"`
	Vars      map[string]any    `json:"vars"`
	Services  []ResolvedService `json:"services"`
	// OwnershipServices retains all assignments for target-side ownership checks.
	// Excluded services remain outside preparation, execution and inspection output.
	OwnershipServices []ResolvedService `json:"-" yaml:"-"`
}

type ResolvedService struct {
	Assignment
	Definition Service `json:"definition"`
}
