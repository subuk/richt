package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/subuk/richt/inventory"
	"github.com/subuk/richt/inventoryyaml"
	"github.com/subuk/richt/source"
	"go.yaml.in/yaml/v3"
)

// Client settings are explicitly supplied by the operator. Remote documents
// cannot select a client file, credential value, CA, or local secret path.
type sourceSettings struct {
	CAFile        string                            `yaml:"ca_file"`
	Auth          []sourceAuth                      `yaml:"auth"`
	SecretSources map[string]inventory.SecretSource `yaml:"secret_sources"`
}

type sourceAuth struct {
	Origin     string           `yaml:"origin"`
	PathPrefix string           `yaml:"path_prefix"`
	Bearer     *credentialRef   `yaml:"bearer"`
	Basic      *basicSourceAuth `yaml:"basic"`
}

type basicSourceAuth struct {
	Username string        `yaml:"username"`
	Password credentialRef `yaml:"password"`
}

type credentialRef struct {
	Env  string `yaml:"env"`
	File string `yaml:"file"`
}

func loadSourceConfig(filename string) (*source.Client, map[string]inventory.SecretSource, error) {
	var settings sourceSettings
	var options source.Options
	if filename != "" {
		filename, err := filepath.Abs(filename)
		if err != nil {
			return nil, nil, errors.New("source config: invalid local path")
		}
		data, err := readClientFile(filename, 1<<20)
		if err != nil {
			return nil, nil, errors.New("source config: cannot read local settings (maximum 1 MiB)")
		}
		var declarations struct {
			SecretSources yaml.Node `yaml:"secret_sources"`
		}
		if err := yaml.Unmarshal(data, &declarations); err != nil {
			return nil, nil, errors.New("source config: invalid YAML settings")
		}
		if declarations.SecretSources.Kind != 0 {
			if err := inventoryyaml.ValidateSecretSourceDeclarations(&declarations.SecretSources); err != nil {
				return nil, nil, errors.New("source config: invalid secret source declarations")
			}
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&settings); err != nil {
			return nil, nil, errors.New("source config: invalid YAML settings")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, nil, errors.New("source config: expected exactly one YAML document")
		}
		if settings.CAFile != "" {
			options.CAFile, err = clientPath(filename, settings.CAFile)
			if err != nil {
				return nil, nil, errors.New("source config: ca_file must be a local path")
			}
		}
		for index, auth := range settings.Auth {
			if (auth.Bearer != nil) == (auth.Basic != nil) {
				return nil, nil, fmt.Errorf("source config: auth entry %d requires exactly one of bearer or basic", index+1)
			}
			a := source.Auth{Origin: auth.Origin, PathPrefix: auth.PathPrefix}
			if auth.Bearer != nil {
				a.BearerToken, err = auth.Bearer.value(filename)
			} else {
				a.Username = auth.Basic.Username
				if a.Username == "" || strings.Contains(a.Username, ":") || strings.IndexFunc(a.Username, unicode.IsControl) >= 0 {
					return nil, nil, fmt.Errorf("source config: auth entry %d has an invalid basic username", index+1)
				}
				a.Password, err = auth.Basic.Password.value(filename)
			}
			if err != nil {
				return nil, nil, fmt.Errorf("source config: auth entry %d: %w", index+1, err)
			}
			options.Auth = append(options.Auth, a)
		}
		for name, binding := range settings.SecretSources {
			if err := inventory.ValidateSecretSource(name, binding); err != nil {
				return nil, nil, errors.New("source config: invalid secret source declaration")
			}
			if binding.Type == "yaml" {
				binding.File, err = clientPath(filename, binding.File)
				if err != nil {
					return nil, nil, errors.New("source config: secret source file must be a local path")
				}
			}
			settings.SecretSources[name] = binding
		}
	}
	client, err := source.New(options)
	if err != nil {
		return nil, nil, fmt.Errorf("source config: %w", err)
	}
	return client, settings.SecretSources, nil
}

func clientPath(declaring, value string) (string, error) {
	if strings.Contains(value, "://") || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", errors.New("invalid local path")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(filepath.Dir(declaring), value)
	}
	return filepath.Clean(value), nil
}

func (ref credentialRef) value(declaring string) (string, error) {
	if (ref.Env != "") == (ref.File != "") {
		return "", errors.New("credential requires exactly one env or file reference")
	}
	var value string
	if ref.Env != "" {
		var present bool
		value, present = os.LookupEnv(ref.Env)
		if !present {
			return "", errors.New("credential environment variable is unavailable")
		}
	} else {
		filename, err := clientPath(declaring, ref.File)
		if err != nil {
			return "", errors.New("credential file must be a local path")
		}
		data, err := readClientFile(filename, 64<<10)
		if err != nil {
			return "", errors.New("cannot read credential file (maximum 64 KiB)")
		}
		// Permit a single trailing line ending from an ordinary text file. Do
		// not trim spaces or otherwise silently change the credential itself.
		value = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	}
	if value == "" || len(value) > 64<<10 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", errors.New("credential must be a nonempty single-line value of at most 64 KiB")
	}
	return value, nil
}

func readClientFile(filename string, limit int64) ([]byte, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("cannot read bounded local file")
	}
	return data, nil
}
