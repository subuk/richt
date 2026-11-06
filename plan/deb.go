package plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/subuk/richt/inventory"
)

// DEBChange fixes an APT request and execution policy. Repository transactions
// and dependencies remain resolved by the target when the request is applied.
type DEBChange struct {
	Manager        string `json:"manager"`
	Package        string `json:"package"`
	Version        string `json:"version,omitempty"`
	State          string `json:"state"`
	CacheMaxAge    string `json:"cache_max_age"`
	LockTimeout    string `json:"lock_timeout"`
	Timeout        string `json:"timeout"`
	ConffilePolicy string `json:"conffile_policy"`
}

// UnmarshalJSON rejects unknown fields and nulls instead of silently turning
// explicitly supplied settings into zero values.
func (c *DEBChange) UnmarshalJSON(data []byte) error {
	type plain DEBChange
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return errors.New("invalid DEB JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return errors.New("invalid DEB JSON")
	}
	for _, raw := range fields {
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil || value == nil || *value == "" {
			return errors.New("DEB settings must be nonempty strings when supplied")
		}
	}
	*c = DEBChange(decoded)
	return nil
}

// Validate checks frozen execution inputs without consulting a target.
func (c *DEBChange) Validate() error {
	if c == nil {
		return errors.New("DEB operation requires a payload")
	}
	if c.Manager != "apt" {
		return errors.New("DEB manager must be apt")
	}
	if err := inventory.ValidateDEBPackage(c.Package); err != nil {
		return err
	}
	if err := inventory.ValidateDEBVersion(c.Version); err != nil {
		return err
	}
	if c.State != "present" && c.State != "absent" {
		return errors.New("DEB state must be present or absent")
	}
	if c.CacheMaxAge != "48h" {
		return errors.New("DEB cache_max_age must be 48h")
	}
	if c.ConffilePolicy != "keep" {
		return errors.New("DEB conffile_policy must be keep")
	}
	for _, input := range []struct{ field, value string }{{"lock_timeout", c.LockTimeout}, {"timeout", c.Timeout}} {
		duration, err := time.ParseDuration(input.value)
		if err != nil || duration <= 0 || duration > 24*time.Hour {
			return fmt.Errorf("DEB %s must be a positive duration of at most 24h", input.field)
		}
	}
	lockTimeout, _ := time.ParseDuration(c.LockTimeout)
	timeout, _ := time.ParseDuration(c.Timeout)
	if lockTimeout > timeout {
		return errors.New("DEB lock_timeout cannot exceed timeout")
	}
	return nil
}
