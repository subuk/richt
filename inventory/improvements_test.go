package inventory

import (
	"strings"
	"testing"
)

func TestMaskTargetsValidatedWithoutObservation(t *testing.T) {
	for _, suffix := range []string{"service", "socket"} {
		t.Run(suffix, func(t *testing.T) {
			yes, no := true, false
			for _, tt := range []struct {
				name    string
				process Process
				config  bool
				invalid bool
			}{
				{"mask stopped", Process{State: "stopped", Masked: &yes}, false, false},
				{"explicit unmask", Process{Masked: &no}, false, false},
				{"default running", Process{Masked: &yes}, false, true},
				{"running", Process{State: "running", Masked: &yes}, false, true},
				{"enabled", Process{State: "stopped", Enabled: &yes, Masked: &yes}, false, true},
				{"owned unit", Process{State: "stopped", Masked: &yes, UnitFile: func() *Config { c := inlineConfig("/etc/systemd/system/demo." + suffix); return &c }()}, false, true},
				{"ordinary owned unit", Process{State: "stopped", Masked: &yes}, true, true},
			} {
				t.Run(tt.name, func(t *testing.T) {
					p := tt.process
					p.Type = "systemd"
					p.Unit = "demo." + suffix
					s := Service{Processes: []Process{namedProcess("demo", p)}}
					if tt.config {
						s.Configs = []Config{namedConfig("unit", inlineConfig("/etc/systemd/system/demo."+suffix))}
					}
					err := validateService(s)
					if (err != nil) != tt.invalid {
						t.Fatalf("validation = %v, invalid = %t", err, tt.invalid)
					}
					if err != nil && !strings.Contains(err.Error(), "masked") {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

func TestValidatorDeclaration(t *testing.T) {
	for _, tt := range []struct {
		name    string
		args    []string
		timeout string
		invalid bool
	}{
		{"valid", []string{"/usr/sbin/sshd", "-t", "-f", "{candidate}"}, "", false},
		{"explicit timeout", []string{"/bin/check", "{candidate}"}, "2s", false},
		{"empty", nil, "", true},
		{"relative executable", []string{"check", "{candidate}"}, "", true},
		{"missing candidate", []string{"/bin/check"}, "", true},
		{"embedded candidate", []string{"/bin/check", "--file={candidate}"}, "", true},
		{"duplicate candidate", []string{"/bin/check", "{candidate}", "{candidate}"}, "", true},
		{"candidate executable", []string{"/{candidate}"}, "", true},
		{"NUL", []string{"/bin/check", "{candidate}", "bad\x00arg"}, "", true},
		{"zero timeout", []string{"/bin/check", "{candidate}"}, "0s", true},
		{"negative timeout", []string{"/bin/check", "{candidate}"}, "-1s", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := inlineConfig("/etc/demo.conf")
			c.Validate = &Validator{Args: tt.args, Timeout: tt.timeout}
			err := validateConfig(c)
			if (err != nil) != tt.invalid {
				t.Fatalf("validation = %v, invalid = %t", err, tt.invalid)
			}
		})
	}
}
