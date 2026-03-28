//go:build darwin || windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	tuiEventHeight   = 10
	tuiEventStoreMax = 400
	tuiMaxInnerW     = 100
	tuiMinInnerW     = 40
	tuiWheelLines    = 3
	tuiPgStep        = 5
	leftColsPad      = 3

	tuiScreenMain int = iota
	tuiScreenSettings
)

const (
	tuiSetPack = iota
	tuiSetCooldown
	tuiSetSpeed
	tuiSetVolume
)

// Built-in pack order for TUI cycling (must match loadEmbeddedPackByID).
var tuiEmbeddedPackOrder = []string{"pain", "sexy", "halo", "lizard"}

const (
	cooldownStep = 50
	speedStep    = 0.05
	minCooldown  = 50
	maxCooldown  = 60000
	minSpeed     = 0.25
	maxSpeed     = 4.0
)

type tickMsg struct{ at time.Time }

type tuiModel struct {
	rt           *mouseLoopRuntime
	packName     string
	tuningLabel  string
	pollInterval time.Duration
	termW, termH int
	events       []string
	scrollBack   int
	hintLine     string

	screen      int // tuiScreenMain or tuiScreenSettings
	settingsSel int // tuiSetCooldown, tuiSetSpeed, tuiSetVolume
}

func newTuiModel(rt *mouseLoopRuntime, packName, tuningLabel string, poll time.Duration, hintLine string) *tuiModel {
	return &tuiModel{
		rt:           rt,
		packName:     packName,
		tuningLabel:  tuningLabel,
		pollInterval: poll,
		termW:        80,
		termH:        24,
		hintLine:     hintLine,
		screen:       tuiScreenMain,
		settingsSel:  tuiSetPack,
	}
}

func soundSnapshot() (cooldown int, speed float64, volScale bool) {
	soundMu.RLock()
	defer soundMu.RUnlock()
	return cooldownMs, speedRatio, volumeScaling
}

func soundSetCooldown(ms int) {
	if ms < minCooldown {
		ms = minCooldown
	}
	if ms > maxCooldown {
		ms = maxCooldown
	}
	soundMu.Lock()
	cooldownMs = ms
	soundMu.Unlock()
	logFileLine(fmt.Sprintf("tui: cooldown=%dms", ms))
}

func soundAdjustCooldown(delta int) {
	cd, _, _ := soundSnapshot()
	soundSetCooldown(cd + delta)
}

func soundSetSpeed(s float64) {
	if s < minSpeed {
		s = minSpeed
	}
	if s > maxSpeed {
		s = maxSpeed
	}
	soundMu.Lock()
	speedRatio = s
	soundMu.Unlock()
	logFileLine(fmt.Sprintf("tui: speed=%.2fx", s))
}

func soundAdjustSpeed(delta float64) {
	_, sp, _ := soundSnapshot()
	soundSetSpeed(sp + delta)
}

func soundSetVolumeScaling(on bool) {
	soundMu.Lock()
	volumeScaling = on
	soundMu.Unlock()
	logFileLine(fmt.Sprintf("tui: volume_scaling=%v", on))
}

func soundToggleVolumeScaling() {
	soundMu.Lock()
	volumeScaling = !volumeScaling
	v := volumeScaling
	soundMu.Unlock()
	logFileLine(fmt.Sprintf("tui: volume_scaling=%v", v))
}

func (m *tuiModel) cyclePack(delta int) {
	if runtimeCustomPack {
		logFileLine("tui: pack switch disabled (started with --custom)")
		return
	}
	n := len(tuiEmbeddedPackOrder)
	idx := 0
	for i, id := range tuiEmbeddedPackOrder {
		if id == m.packName {
			idx = i
			break
		}
	}
	idx = (idx + delta%n + n) % n
	p, err := loadEmbeddedPackByID(tuiEmbeddedPackOrder[idx])
	if err != nil {
		logFileLine("tui: pack: " + err.Error())
		return
	}
	m.rt.switchToPack(p)
	m.packName = p.name
	logFileLine(fmt.Sprintf("tui: pack=%s", p.name))
}

func (m *tuiModel) tickCmd() tea.Cmd {
	return tea.Every(m.pollInterval, func(t time.Time) tea.Msg {
		return tickMsg{at: t}
	})
}

func (m *tuiModel) Init() tea.Cmd {
	return m.tickCmd()
}

func (m *tuiModel) maxScrollBack() int {
	n := len(m.events)
	if n <= tuiEventHeight {
		return 0
	}
	return n - tuiEventHeight
}

func (m *tuiModel) clampScroll() {
	if m.scrollBack < 0 {
		m.scrollBack = 0
	}
	if mx := m.maxScrollBack(); m.scrollBack > mx {
		m.scrollBack = mx
	}
}

func (m *tuiModel) firstVisibleIndex() int {
	n := len(m.events)
	if n == 0 {
		return 0
	}
	if n <= tuiEventHeight {
		return 0
	}
	lastStart := n - tuiEventHeight
	start := lastStart - m.scrollBack
	if start < 0 {
		return 0
	}
	return start
}

func (m *tuiModel) pushEvent(line string) {
	m.events = append(m.events, line)
	if len(m.events) > tuiEventStoreMax {
		drop := len(m.events) - tuiEventStoreMax
		m.events = m.events[drop:]
	}
	m.clampScroll()
}

func (m *tuiModel) innerWidth() int {
	w := m.termW - 4
	if w < tuiMinInnerW {
		w = tuiMinInnerW
	}
	if w > tuiMaxInnerW {
		w = tuiMaxInnerW
	}
	return w
}

// Main panel layout (1-based terminal rows).
func (m *tuiModel) layoutMainRows1Based() (titleY, eventLastY, footerBtnY, innerLastY int) {
	titleY = 2
	eventLastY = 2 + tuiEventHeight
	footerBtnY = eventLastY + 1
	innerLastY = footerBtnY + 1
	return titleY, eventLastY, footerBtnY, innerLastY
}

// Settings panel: border, title, pack + 3 value rows, hint.
func (m *tuiModel) layoutSettingsRows1Based() (titleY, rowPack, rowCD, rowSp, rowVol, hintY int) {
	titleY = 2
	rowPack = 3
	rowCD = 4
	rowSp = 5
	rowVol = 6
	hintY = 7
	return titleY, rowPack, rowCD, rowSp, rowVol, hintY
}

func (m *tuiModel) scrollWheel(delta int) {
	if delta > 0 {
		m.scrollBack += delta
	} else {
		m.scrollBack += delta
		if m.scrollBack < 0 {
			m.scrollBack = 0
		}
	}
	m.clampScroll()
}

func (m *tuiModel) handleMainFooterClick(x1Based int) tea.Cmd {
	iw := m.innerWidth()
	ix := x1Based - leftColsPad
	if ix < 0 {
		ix = 0
	}
	if ix > iw {
		ix = iw
	}
	third := max(1, iw/3)
	switch {
	case ix < third:
		pausedMu.Lock()
		paused = true
		pausedMu.Unlock()
		logFileLine("tui: paused (mouse)")
	case ix < 2*third:
		pausedMu.Lock()
		paused = false
		pausedMu.Unlock()
		logFileLine("tui: resumed (mouse)")
	default:
		return tea.Quit
	}
	return nil
}

func (m *tuiModel) settingsAdjust(dir int) {
	switch m.settingsSel {
	case tuiSetPack:
		m.cyclePack(dir)
	case tuiSetCooldown:
		soundAdjustCooldown(dir * cooldownStep)
	case tuiSetSpeed:
		soundAdjustSpeed(float64(dir) * speedStep)
	case tuiSetVolume:
		if dir > 0 {
			soundSetVolumeScaling(true)
		} else if dir < 0 {
			soundSetVolumeScaling(false)
		}
	}
}

func (m *tuiModel) updateSettingsMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	_, rowPack, rowCD, rowSp, rowVol, hintY := m.layoutSettingsRows1Based()
	y := msg.Y
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if msg.Action == tea.MouseActionPress && y >= rowPack && y <= hintY {
			m.settingsAdjust(+1)
		}
		return m, nil
	case tea.MouseButtonWheelDown:
		if msg.Action == tea.MouseActionPress && y >= rowPack && y <= hintY {
			m.settingsAdjust(-1)
		}
		return m, nil
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		switch y {
		case rowPack:
			m.settingsSel = tuiSetPack
		case rowCD:
			m.settingsSel = tuiSetCooldown
		case rowSp:
			m.settingsSel = tuiSetSpeed
		case rowVol:
			m.settingsSel = tuiSetVolume
		}
		return m, nil
	default:
		return m, nil
	}
}

func (m *tuiModel) updateMainMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	titleY, _, footerBtnY, innerLastY := m.layoutMainRows1Based()
	y := msg.Y
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if msg.Action == tea.MouseActionPress && y >= titleY && y <= innerLastY {
			m.scrollWheel(tuiWheelLines)
		}
		return m, nil
	case tea.MouseButtonWheelDown:
		if msg.Action == tea.MouseActionPress && y >= titleY && y <= innerLastY {
			m.scrollWheel(-tuiWheelLines)
		}
		return m, nil
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		switch {
		case y == titleY:
			pausedMu.Lock()
			paused = !paused
			pausedMu.Unlock()
			logFileLine("tui: pause toggled (mouse title)")
		case y == footerBtnY:
			if cmd := m.handleMainFooterClick(msg.X); cmd != nil {
				return m, cmd
			}
		}
		return m, nil
	default:
		return m, nil
	}
}

func (m *tuiModel) updateSettingsKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := msg.String()
	switch s {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc", "o":
		m.screen = tuiScreenMain
		return m, nil
	case "up", "k":
		m.settingsSel = (m.settingsSel + 3) % 4
	case "down", "j":
		m.settingsSel = (m.settingsSel + 1) % 4
	case "tab":
		m.settingsSel = (m.settingsSel + 1) % 4
	case "shift+tab":
		m.settingsSel = (m.settingsSel + 3) % 4
	case "left", "[":
		m.settingsAdjust(-1)
	case "right", "]":
		m.settingsAdjust(+1)
	case ",":
		if m.settingsSel == tuiSetSpeed {
			soundAdjustSpeed(-speedStep)
		}
	case ".":
		if m.settingsSel == tuiSetSpeed {
			soundAdjustSpeed(speedStep)
		}
	case "v":
		soundToggleVolumeScaling()
	case " ":
		switch m.settingsSel {
		case tuiSetPack:
			if !runtimeCustomPack {
				m.cyclePack(+1)
			}
		case tuiSetVolume:
			soundToggleVolumeScaling()
		}
	default:
		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case '-', '_':
				m.settingsAdjust(-1)
			case '=', '+':
				m.settingsAdjust(+1)
			}
		}
	}
	return m, nil
}

func (m *tuiModel) updateMainKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := msg.String()
	switch s {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "o":
		m.screen = tuiScreenSettings
		m.settingsSel = tuiSetPack
		return m, nil
	case "p":
		pausedMu.Lock()
		paused = true
		pausedMu.Unlock()
		logFileLine("tui: paused (keyboard)")
	case "r":
		pausedMu.Lock()
		paused = false
		pausedMu.Unlock()
		logFileLine("tui: resumed (keyboard)")
	case " ":
		pausedMu.Lock()
		paused = !paused
		pausedMu.Unlock()
		logFileLine("tui: pause toggled (keyboard space)")
	case "up", "k":
		m.scrollBack++
		m.clampScroll()
	case "down", "j":
		if m.scrollBack > 0 {
			m.scrollBack--
		}
	case "pgup", "b":
		m.scrollBack += tuiPgStep
		m.clampScroll()
	case "pgdown", "f":
		m.scrollBack -= tuiPgStep
		if m.scrollBack < 0 {
			m.scrollBack = 0
		}
	case "home":
		m.scrollBack = m.maxScrollBack()
	case "end":
		m.scrollBack = 0
	default:
		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case 'g':
				m.scrollBack = m.maxScrollBack()
			case 'G':
				m.scrollBack = 0
			}
		}
	}
	return m, nil
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termW = msg.Width
		m.termH = msg.Height
		if m.termW < 20 {
			m.termW = 80
		}
		if m.termH < 5 {
			m.termH = 24
		}
		return m, nil

	case tickMsg:
		if lines := m.rt.tick(msg.at); len(lines) > 0 {
			for _, ln := range lines {
				m.pushEvent(ln)
			}
		}
		return m, m.tickCmd()

	case tea.MouseMsg:
		if m.screen == tuiScreenSettings {
			return m.updateSettingsMouse(msg)
		}
		return m.updateMainMouse(msg)

	case tea.KeyMsg:
		if m.screen == tuiScreenSettings {
			return m.updateSettingsKeys(msg)
		}
		return m.updateMainKeys(msg)
	}
	return m, nil
}

func (m *tuiModel) visibleEventLines(iw int) []string {
	n := len(m.events)
	start := m.firstVisibleIndex()
	out := make([]string, 0, tuiEventHeight)
	for i := 0; i < tuiEventHeight; i++ {
		idx := start + i
		if idx < n {
			s := strings.ReplaceAll(m.events[idx], "\n", " ")
			out = append(out, ansi.Truncate(s, iw, "…"))
		} else {
			out = append(out, strings.Repeat(" ", iw))
		}
	}
	return out
}

func (m *tuiModel) viewMain() string {
	iw := m.innerWidth()
	cd, _, _ := soundSnapshot()

	pausedMu.RLock()
	isPaused := paused
	pausedMu.RUnlock()
	pausedStr := "running"
	if isPaused {
		pausedStr = "paused"
	}

	title := fmt.Sprintf(" spank %s | pack=%s | %s | cd=%dms | v%s · o settings · title: pause ",
		m.tuningLabel, m.packName, pausedStr, cd, version)
	if len(title) > iw+8 {
		title = fmt.Sprintf(" %s | %s | cd=%dms | v%s · o=settings ", m.packName, pausedStr, cd, version)
	}
	title = ansi.Truncate(title, iw, "…")

	eventLines := m.visibleEventLines(iw)

	scrollHint := ""
	if mx := m.maxScrollBack(); mx > 0 {
		scrollHint = fmt.Sprintf(" scroll %d/%d ", m.scrollBack, mx)
	}
	footerLeft := lipgloss.NewStyle().Reverse(true).Padding(0, 1).Render(" Pause ")
	footerMid := lipgloss.NewStyle().Reverse(true).Padding(0, 1).Render(" Resume ")
	footerRight := lipgloss.NewStyle().Reverse(true).Padding(0, 1).Render(" Quit ")
	footer := lipgloss.JoinHorizontal(lipgloss.Top, footerLeft, " ", footerMid, " ", footerRight)
	footer = ansi.Truncate(footer, iw, "…")
	meta := "o sound settings · keys ↑↓/jk PgUp/Pg · home/g end/G · p r space · wheel scroll" + scrollHint
	if h := strings.TrimSpace(m.hintLine); h != "" {
		meta = h + " · " + meta
	}
	footerMeta := lipgloss.NewStyle().Faint(true).Render(ansi.Truncate(meta, iw, "…"))

	border := lipgloss.RoundedBorder()
	box := lipgloss.NewStyle().
		Border(border).
		BorderForeground(lipgloss.Color("62")).
		Padding(0, 1).
		Width(iw + 2)

	blocks := []string{
		lipgloss.NewStyle().Bold(true).Render(title),
		strings.Join(eventLines, "\n"),
		footer,
		footerMeta,
	}

	inner := lipgloss.JoinVertical(lipgloss.Left, blocks...)
	return box.Render(inner)
}

func (m *tuiModel) viewSettings() string {
	iw := m.innerWidth()
	cd, sp, vs := soundSnapshot()
	vol := "off"
	if vs {
		vol = "on"
	}

	mark := func(row int) string {
		if m.settingsSel == row {
			return "> "
		}
		return "  "
	}

	var linePack string
	if runtimeCustomPack {
		linePack = fmt.Sprintf("%sSound pack        %s   (custom; CLI only)", mark(tuiSetPack), m.packName)
	} else {
		linePack = fmt.Sprintf("%sSound pack        %s   ←/→ sexy·halo·pain·lizard", mark(tuiSetPack), m.packName)
	}
	lineCD := fmt.Sprintf("%sCooldown (ms)     %d   ←/→ or [ ]  ±%d", mark(tuiSetCooldown), cd, cooldownStep)
	lineSp := fmt.Sprintf("%sSpeed (×)         %.2f  , . fine  (%.2f–%.2f)", mark(tuiSetSpeed), sp, minSpeed, maxSpeed)
	lineVol := fmt.Sprintf("%sVolume × hold     %s   (v) toggle", mark(tuiSetVolume), vol)

	linePack = ansi.Truncate(linePack, iw, "…")
	lineCD = ansi.Truncate(lineCD, iw, "…")
	lineSp = ansi.Truncate(lineSp, iw, "…")
	lineVol = ansi.Truncate(lineVol, iw, "…")

	title := lipgloss.NewStyle().Bold(true).Render(ansi.Truncate(" Settings · sound · esc/o back · q quit ", iw, "…"))
	hint := lipgloss.NewStyle().Faint(true).Render(ansi.Truncate(
		"↑↓/tab row · ←→ / wheel · space: next pack or toggle vol · v: vol", iw, "…"))

	border := lipgloss.RoundedBorder()
	box := lipgloss.NewStyle().
		Border(border).
		BorderForeground(lipgloss.Color("214")).
		Padding(0, 1).
		Width(iw + 2)

	inner := lipgloss.JoinVertical(lipgloss.Left,
		title,
		lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(linePack),
		lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(lineCD),
		lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(lineSp),
		lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(lineVol),
		hint,
	)
	return box.Render(inner)
}

func (m *tuiModel) View() string {
	if m.screen == tuiScreenSettings {
		return m.viewSettings()
	}
	return m.viewMain()
}

func runWindowTUI(ctx context.Context, pack *soundPack, tuning runtimeTuning) error {
	rt := &mouseLoopRuntime{
		Pack:    pack,
		Tuning:  tuning,
		Tracker: newSlapTracker(pack, tuning.cooldown),
	}

	presetLabel := "default"
	if fastMode {
		presetLabel = "fast"
	}

	hint := ""
	if runtime.GOOS == "windows" {
		hint = "Tip: if the console pauses (Select/Quick Edit), press Esc or turn off Quick Edit in cmd properties."
	}

	m := newTuiModel(rt, pack.name, presetLabel, tuning.pollInterval, hint)
	p := tea.NewProgram(m,
		tea.WithContext(ctx),
		tea.WithMouseCellMotion(),
	)
	_, err := p.Run()
	if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return err
	}
	fmt.Fprintln(os.Stderr, "bye!")
	return nil
}
