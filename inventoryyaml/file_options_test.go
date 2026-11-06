package inventoryyaml

import "testing"

func TestTemplateWriteOptionsAndOwnedFiles(t *testing.T) {
	for _, external := range []bool{false, true} {
		in, err := loadServiceFixture(t, `configs:
  - path: /etc/default.conf
    template: {inline: text}
  - path: /etc/custom.conf
    atomic_writes: false
    backup: true
    template: {inline: text}
processes:
  - type: systemd
    unit: app.service
    unit_file:
      atomic_writes: false
      backup: true
      template: {inline: unit}
    overrides:
      extra.conf:
        atomic_writes: true
        backup: true
        template: {inline: dropin}
`, external)
		if err != nil {
			t.Fatal(err)
		}
		s := in.Services["app"]
		if s.Configs[0].AtomicWrites != nil || s.Configs[0].Backup {
			t.Fatal("omitted selections changed")
		}
		if s.Configs[1].AtomicWrites == nil || *s.Configs[1].AtomicWrites || !s.Configs[1].Backup {
			t.Fatal("config options lost")
		}
		p := s.Processes[0]
		if p.UnitFile.AtomicWrites == nil || *p.UnitFile.AtomicWrites || !p.UnitFile.Backup {
			t.Fatal("unit options lost")
		}
		dropin := p.Overrides["extra.conf"]
		if dropin.AtomicWrites == nil || !*dropin.AtomicWrites || !dropin.Backup {
			t.Fatal("dropin options lost")
		}
	}
}
