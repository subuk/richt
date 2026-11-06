package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/subuk/richt/engine"
	"github.com/subuk/richt/plan"
	"golang.org/x/term"
)

// Progress only consumes display metadata. In particular, it never reads task
// payloads or prints command output. Event errors are display-safe categories
// supplied by the plan package, never subprocess diagnostics.
type progress struct {
	mu                                         sync.Mutex
	out                                        io.Writer
	mode, phase                                string
	now                                        func() time.Time
	size                                       func() (int, int)
	started, lastLine                          time.Time
	active                                     map[string]activeTask
	preparation                                string
	total, succeeded, skipped, failed, blocked int
	lines                                      int
	err                                        error
	stopped                                    bool
	done, joined                               chan struct{}
	stopTick                                   func()
}
type activeTask struct {
	label   string
	started time.Time
}
type progressSettings struct {
	now      func() time.Time
	ticks    <-chan time.Time
	stopTick func()
	size     func() (int, int)
	terminal bool
}

func newProgress(out io.Writer, mode, phase string) *progress {
	settings := progressSettings{now: time.Now}
	if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		settings.terminal = true
		settings.size = func() (int, int) {
			w, h, err := term.GetSize(int(f.Fd()))
			if err != nil {
				return 80, 24
			}
			return w, h
		}
	}
	if mode != "off" {
		ticker := time.NewTicker(time.Second)
		settings.ticks, settings.stopTick = ticker.C, ticker.Stop
	}
	return newProgressWithSettings(out, mode, phase, settings)
}

func newProgressWithSettings(out io.Writer, mode, phase string, settings progressSettings) *progress {
	if mode == "" || mode == "auto" {
		if settings.terminal {
			mode = "tty"
		} else {
			mode = "plain"
		}
	}
	if settings.now == nil {
		settings.now = time.Now
	}
	if settings.size == nil {
		settings.size = func() (int, int) { return 80, 24 }
	}
	if settings.stopTick == nil {
		settings.stopTick = func() {}
	}
	now := settings.now()
	p := &progress{out: out, mode: mode, phase: phase, now: settings.now, size: settings.size, started: now, lastLine: now, active: make(map[string]activeTask), done: make(chan struct{}), joined: make(chan struct{}), stopTick: settings.stopTick}
	if mode != "off" {
		p.lineLocked(phase + " started")
		if mode == "tty" {
			p.renderLocked(now)
		}
	}
	go func() {
		defer close(p.joined)
		for {
			select {
			case <-p.done:
				return
			case now, ok := <-settings.ticks:
				if !ok {
					return
				}
				p.refresh(now)
			}
		}
	}()
	return p
}

func (p *progress) prepare(e engine.PrepareEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped || p.mode == "off" {
		return
	}
	label := cleanProgress(e.Phase)
	if e.Host != "" {
		label += " host=" + cleanProgress(e.Host)
	}
	if e.Service != "" {
		label += " service=" + cleanProgress(e.Service)
	}
	if label == p.preparation {
		return
	}
	p.preparation = label
	p.clearLocked()
	if p.mode == "plain" {
		p.lineLocked("Preparing: " + label)
	}
	if p.mode == "tty" {
		p.renderLocked(p.now())
	}
}

func (p *progress) apply(e plan.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped || p.mode == "off" {
		return
	}
	now := p.now()
	key := e.Host + "\x00" + e.TaskID
	label := "host=" + cleanProgress(e.Host)
	if e.Service != "" {
		label += " service=" + cleanProgress(e.Service)
	}
	if e.Component != "" {
		label += " component=" + cleanProgress(e.Component)
	}
	label += " task=" + cleanProgress(e.TaskID)
	if e.Operation != "" {
		label += " operation=" + cleanProgress(e.Operation)
	}
	line := ""
	switch e.Kind {
	case plan.EventApplyStarted:
		p.total = e.Total
		line = fmt.Sprintf("Apply: %d task(s)", e.Total)
	case plan.EventTaskStarted:
		p.active[key] = activeTask{label, now}
		if p.mode == "plain" {
			line = "Started " + label
		}
	case plan.EventTaskSucceeded:
		delete(p.active, key)
		p.succeeded++
		line = "Completed " + label
		if e.Persistence == plan.PersistenceFailed {
			line = "Operation completed; progress save failed: " + label
		}
	case plan.EventTaskFailed:
		delete(p.active, key)
		p.failed++
		line = "Failed " + label
		if e.Error != "" {
			if p.mode == "tty" {
				line = "Failed: " + cleanProgress(e.Error) + "; " + label
			} else {
				line += ": " + cleanProgress(e.Error)
			}
		}
	case plan.EventTaskSkipped:
		p.skipped++
		line = "Previously completed " + label
	case plan.EventTaskBlocked:
		delete(p.active, key)
		p.blocked++
		line = "Blocked " + label
		if e.Error != "" {
			if p.mode == "tty" {
				line = "Blocked: " + cleanProgress(e.Error) + "; " + label
			} else {
				line += ": " + cleanProgress(e.Error)
			}
		}
	case plan.EventApplyCanceled:
		line = "Apply canceled; waiting for active tasks"
	case plan.EventPersistenceFailed:
		line = "Progress save failed; stopping further tasks"
	case plan.EventApplyFinished:
		line = "Apply finished"
		if e.Error != "" {
			line = "Apply finished with errors"
		}
	}
	p.clearLocked()
	if line != "" {
		p.lineLocked(line)
	}
	if p.mode == "tty" {
		p.renderLocked(now)
	}
}

func (p *progress) factHost(host string, started, failed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped || p.mode == "off" {
		return
	}
	label := "host=" + cleanProgress(host)
	line := "Completed facts " + label
	if started {
		p.active[host] = activeTask{label, p.now()}
		line = "Collecting facts " + label
	} else {
		delete(p.active, host)
		if failed {
			p.failed++
			line = "Failed facts " + label
		} else {
			p.succeeded++
		}
	}
	p.clearLocked()
	if p.mode == "plain" || !started {
		p.lineLocked(line)
	}
	if p.mode == "tty" {
		p.renderLocked(p.now())
	}
}

func (p *progress) refresh(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped || p.mode == "off" {
		return
	}
	if p.mode == "tty" {
		p.clearLocked()
		p.renderLocked(now)
		return
	}
	if now.Sub(p.lastLine) >= 30*time.Second {
		line := p.summaryLocked(now)
		labels := p.labelsLocked(now)
		if len(labels) > 0 {
			line += "; " + strings.Join(labels, "; ")
		}
		p.lineLocked(line)
		p.lastLine = now
	}
}

func (p *progress) summaryLocked(now time.Time) string {
	elapsed := progressElapsed(now.Sub(p.started))
	if p.phase == "Preparing" {
		return fmt.Sprintf("Preparing elapsed=%s", elapsed)
	}
	if p.phase == "Collecting facts" {
		return fmt.Sprintf("Collecting facts total=%d completed=%d failed=%d active=%d elapsed=%s", p.total, p.succeeded, p.failed, len(p.active), elapsed)
	}
	pending := max(0, p.total-p.succeeded-p.skipped-p.failed-p.blocked-len(p.active))
	if p.mode == "tty" {
		return fmt.Sprintf("Apply %s done=%d/%d prior=%d fail=%d block=%d run=%d wait=%d", elapsed, p.succeeded, p.total, p.skipped, p.failed, p.blocked, len(p.active), pending)
	}
	return fmt.Sprintf("Applying total=%d completed=%d previously_completed=%d failed=%d blocked=%d active=%d pending=%d elapsed=%s", p.total, p.succeeded, p.skipped, p.failed, p.blocked, len(p.active), pending, elapsed)
}
func (p *progress) labelsLocked(now time.Time) []string {
	if p.phase == "Preparing" {
		if p.preparation != "" {
			return []string{p.preparation}
		}
		return nil
	}
	keys := make([]string, 0, len(p.active))
	for key := range p.active {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	labels := make([]string, 0, len(keys))
	for _, key := range keys {
		t := p.active[key]
		labels = append(labels, "elapsed="+progressElapsed(now.Sub(t.started))+" "+t.label)
	}
	return labels
}
func (p *progress) renderLocked(now time.Time) {
	if p.err != nil {
		return
	}
	width, height := p.size()
	if width < 2 || height < 2 {
		return
	}
	rows := append([]string{p.summaryLocked(now)}, p.labelsLocked(now)...)
	capacity := max(1, height-2)
	if len(rows) > capacity {
		omitted := len(rows) - capacity + 1
		rows = rows[:capacity]
		if capacity > 1 {
			rows[capacity-1] = fmt.Sprintf("... %d more active task(s)", omitted)
		}
	}
	for _, row := range rows {
		p.writeLocked(clipProgress(row, width-1) + "\n")
	}
	p.lines = len(rows)
}
func (p *progress) clearLocked() {
	if p.lines == 0 {
		return
	}
	p.writeLocked(fmt.Sprintf("\x1b[%dA\r\x1b[J", p.lines))
	p.lines = 0
}
func (p *progress) lineLocked(line string) {
	if p.mode == "tty" {
		width, _ := p.size()
		line = clipProgress(line, max(0, width-1))
	}
	p.writeLocked(line + "\n")
	p.lastLine = p.now()
}
func (p *progress) writeLocked(s string) {
	if p.err != nil {
		return
	}
	_, p.err = io.WriteString(p.out, s)
}
func (p *progress) stop() error {
	p.mu.Lock()
	if !p.stopped {
		p.stopped = true
		p.clearLocked()
		close(p.done)
		p.stopTick()
	}
	err := p.err
	p.mu.Unlock()
	<-p.joined
	if err != nil {
		return fmt.Errorf("write progress: %w", err)
	}
	return nil
}
func progressElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Truncate(time.Second).String()
}

// Remove control characters, including terminal escape sequences, from labels.
func cleanProgress(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
}
func progressRuneWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a || r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe10 && r <= 0xfe6f || r >= 0xff00 && r <= 0xff60 || r >= 0xffe0 && r <= 0xffe6 || r >= 0x1f000 && r <= 0x1ffff || r >= 0x20000) {
		return 2
	}
	return 1
}
func clipProgress(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = cleanProgress(s)
	used := 0
	for i, r := range s {
		next := progressRuneWidth(r)
		if used+next > width {
			return s[:i]
		}
		used += next
	}
	return s
}
