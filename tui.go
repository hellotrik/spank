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
	// Below this inner width, footer uses compact P/R/Q labels so row fits without truncation.
	tuiFooterFullMinW = 36
	tuiWheelLines     = 3
	tuiPgStep         = 5

	// Rounded border (1 col) + box horizontal Padding(0,1) (1 col) before inner text.
	tuiInnerLeftCol = 2

	tuiScreenMain int = iota
	tuiScreenSettings
)

const (
	tuiToolbarHoverNone   = -1
	tuiToolbarHoverPause  = 0
	tuiToolbarHoverResume = 1
	tuiToolbarHoverQuit   = 2
)

const (
	tuiSetPack = iota
	tuiSetCooldown
	tuiSetSpeed
	tuiSetVolume
	tuiSetInputMode
	tuiSetLog
)

// Built-in pack order for TUI cycling (must match loadEmbeddedPackByID).
var tuiEmbeddedPackOrder = []string{"pain", "sexy", "halo", "lizard", "sward"}

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

	// Last cell from tea.MouseMsg (0-based); for on-screen debug overlay.
	mouseX, mouseY int
	mouseSeen      bool

	// Which toolbar segment is under the pointer (tuiToolbarHover*); main only.
	toolbarHover int

	// Row index of the P/R/Q line from the last footerButtonRow() scan (0-based); -1 until known.
	lastToolbarRow int
}

func newTuiModel(rt *mouseLoopRuntime, packName, tuningLabel string, poll time.Duration, hintLine string) *tuiModel {
	return &tuiModel{
		rt:             rt,
		packName:       packName,
		tuningLabel:    tuningLabel,
		pollInterval:   poll,
		termW:          80,
		termH:          24,
		hintLine:       hintLine,
		screen:         tuiScreenMain,
		settingsSel:    tuiSetPack,
		toolbarHover:   tuiToolbarHoverNone,
		lastToolbarRow: -1,
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
	persistSpankConfig()
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
	persistSpankConfig()
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
	persistSpankConfig()
}

func soundToggleVolumeScaling() {
	soundMu.Lock()
	volumeScaling = !volumeScaling
	v := volumeScaling
	soundMu.Unlock()
	logFileLine(fmt.Sprintf("tui: volume_scaling=%v", v))
	persistSpankConfig()
}

func inputListenSnapshot() (mouseOn, kbOn bool) {
	inputListenMu.RLock()
	defer inputListenMu.RUnlock()
	return listenMouse, listenKeyboard
}

func inputModeLabel() string {
	m, k := inputListenSnapshot()
	switch {
	case m && k:
		return "mouse + keyboard"
	case m:
		return "mouse only"
	case k:
		return "keyboard only"
	default:
		return "?"
	}
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
	embeddedPackForConfig = p.name
	logFileLine(fmt.Sprintf("tui: pack=%s", p.name))
	persistSpankConfig()
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
	// Must track real terminal width: forcing a minimum wider than termW-4 breaks
	// mouse hit-testing and ansi.Truncate vs actual columns when the window is narrow.
	w := m.termW - 4
	if w < 1 {
		w = 1
	}
	if w > tuiMaxInnerW {
		w = tuiMaxInnerW
	}
	return w
}

// Main panel layout: bubbletea MouseMsg Y is 0-based (top line of terminal = 0).
// viewMain: top border, Pause/Resume/Quit row, title, tuiEventHeight event lines, meta, bottom border.
func (m *tuiModel) layoutMainRows0Based() (titleY, eventLastY, footerBtnY, innerLastY int) {
	footerBtnY = 1
	titleY = 2
	eventLastY = titleY + tuiEventHeight // last event row (10 lines after title)
	innerLastY = eventLastY + 1
	return titleY, eventLastY, footerBtnY, innerLastY
}

// Settings panel: border, title, pack + sound rows + input rows, hint (same 0-based Y as MouseMsg).
func (m *tuiModel) layoutSettingsRows0Based() (titleY, rowPack, rowCD, rowSp, rowVol, rowInput, rowLog, hintY int) {
	titleY = 1
	rowPack = 2
	rowCD = 3
	rowSp = 4
	rowVol = 5
	rowInput = 6
	rowLog = 7
	hintY = 8
	return titleY, rowPack, rowCD, rowSp, rowVol, rowInput, rowLog, hintY
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

func footerButtonLabels(compact bool) [3]string {
	if compact {
		return [3]string{" P ", " R ", " Q "}
	}
	return [3]string{" Pause ", " Resume ", " Quit "}
}

// footerButtonSegments must match viewMain toolbar rendering (widths drive hit-testing).
func (m *tuiModel) footerButtonSegments() (left, mid, right string) {
	st := lipgloss.NewStyle().Reverse(true).Padding(0, 1)
	labels := footerButtonLabels(m.innerWidth() < tuiFooterFullMinW)
	return st.Render(labels[0]), st.Render(labels[1]), st.Render(labels[2])
}

// renderToolbar draws Pause/Resume/Quit with stronger reverse (bold) on the hovered segment.
func (m *tuiModel) renderToolbar() string {
	iw := m.innerWidth()
	compact := iw < tuiFooterFullMinW
	labels := footerButtonLabels(compact)
	base := lipgloss.NewStyle().Reverse(true).Padding(0, 1)
	hi := lipgloss.NewStyle().Reverse(true).Bold(true).Padding(0, 1)
	parts := make([]string, 3)
	for i := 0; i < 3; i++ {
		st := base
		if m.toolbarHover == i {
			st = hi
		}
		parts[i] = st.Render(labels[i])
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts[0], " ", parts[1], " ", parts[2])
}

func (m *tuiModel) toolbarHitIndex(x int) int {
	left, mid, right := m.footerButtonSegments()
	w1, w2, w3 := lipgloss.Width(left), lipgloss.Width(mid), lipgloss.Width(right)
	const gap = 1
	ix := x - tuiInnerLeftCol
	if ix < 0 {
		return tuiToolbarHoverNone
	}
	switch {
	case ix < w1:
		return tuiToolbarHoverPause
	case ix < w1+gap:
		return tuiToolbarHoverNone
	case ix < w1+gap+w2:
		return tuiToolbarHoverResume
	case ix < w1+gap+w2+gap:
		return tuiToolbarHoverNone
	case ix < w1+gap+w2+gap+w3:
		return tuiToolbarHoverQuit
	default:
		return tuiToolbarHoverNone
	}
}

func (m *tuiModel) syncToolbarHover(msg tea.MouseMsg) {
	fr := m.footerButtonRow()
	m.lastToolbarRow = fr
	if msg.Y != fr {
		m.toolbarHover = tuiToolbarHoverNone
		return
	}
	m.toolbarHover = m.toolbarHitIndex(msg.X)
}

// mainMouseDebugPrefix is prepended to the meta line: live ptr + toolbar row + P/R/Q column spans (half-open).
func (m *tuiModel) mainMouseDebugPrefix() string {
	if !m.mouseSeen {
		return ""
	}
	btnY := m.lastToolbarRow
	if btnY < 0 {
		_, _, btnY, _ = m.layoutMainRows0Based()
	}
	left, mid, right := m.footerButtonSegments()
	w1, w2, w3 := lipgloss.Width(left), lipgloss.Width(mid), lipgloss.Width(right)
	const gap = 1
	c := tuiInnerLeftCol
	pLo, pHi := c, c+w1
	rLo, rHi := c+w1+gap, c+w1+gap+w2
	qLo, qHi := c+w1+gap+w2+gap, c+w1+gap+w2+gap+w3
	return fmt.Sprintf("ptr(%d,%d) row=%d P[%d,%d) R[%d,%d) Q[%d,%d) · ",
		m.mouseX, m.mouseY, btnY, pLo, pHi, rLo, rHi, qLo, qHi)
}

// footerButtonRow finds the terminal row (0-based) of the Pause/Resume/Quit toolbar line by scanning
// the current view so title wrapping cannot desync fixed row math.
func (m *tuiModel) footerButtonRow() int {
	view := strings.ReplaceAll(m.View(), "\r", "")
	for i, line := range strings.Split(view, "\n") {
		plain := ansi.Strip(line)
		if (strings.Contains(plain, "Pause") && strings.Contains(plain, "Resume")) ||
			(strings.Contains(plain, " P ") && strings.Contains(plain, " R ")) {
			return i
		}
	}
	_, _, fb, _ := m.layoutMainRows0Based()
	return fb
}

func (m *tuiModel) handleMainFooterClick(x int) tea.Cmd {
	switch m.toolbarHitIndex(x) {
	case tuiToolbarHoverPause:
		pausedMu.Lock()
		paused = true
		pausedMu.Unlock()
		logFileLine("tui: paused (mouse)")
	case tuiToolbarHoverResume:
		pausedMu.Lock()
		paused = false
		pausedMu.Unlock()
		logFileLine("tui: resumed (mouse)")
	case tuiToolbarHoverQuit:
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
	case tuiSetInputMode:
		cycleInputMode(dir)
	case tuiSetLog:
		if appRunContext == nil {
			break
		}
		if dir > 0 {
			setLogEnabled(appRunContext, true)
		} else if dir < 0 {
			setLogEnabled(appRunContext, false)
		}
	}
}

func (m *tuiModel) updateSettingsMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	_, rowPack, rowCD, rowSp, rowVol, rowInput, rowLog, hintY := m.layoutSettingsRows0Based()
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
		if msg.Action != tea.MouseActionPress && msg.Action != tea.MouseActionRelease {
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
		case rowInput:
			m.settingsSel = tuiSetInputMode
		case rowLog:
			m.settingsSel = tuiSetLog
		}
		return m, nil
	default:
		return m, nil
	}
}

func (m *tuiModel) updateMainMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	titleY, _, _, innerLastY := m.layoutMainRows0Based()
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
		if msg.Action != tea.MouseActionPress && msg.Action != tea.MouseActionRelease {
			return m, nil
		}
		switch {
		case y == titleY:
			pausedMu.Lock()
			paused = !paused
			pausedMu.Unlock()
			logFileLine("tui: pause toggled (mouse title)")
		case m.lastToolbarRow >= 0 && y == m.lastToolbarRow:
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
		m.toolbarHover = tuiToolbarHoverNone
		m.lastToolbarRow = -1
		return m, nil
	case "up", "k":
		m.settingsSel = (m.settingsSel + 5) % 6
	case "down", "j":
		m.settingsSel = (m.settingsSel + 1) % 6
	case "tab":
		m.settingsSel = (m.settingsSel + 1) % 6
	case "shift+tab":
		m.settingsSel = (m.settingsSel + 5) % 6
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
		case tuiSetInputMode:
			cycleInputMode(1)
		case tuiSetLog:
			if appRunContext != nil {
				setLogEnabled(appRunContext, !logToFile)
			}
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
		m.toolbarHover = tuiToolbarHoverNone
		m.lastToolbarRow = -1
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
		m.mouseX, m.mouseY = msg.X, msg.Y
		m.mouseSeen = true
		if m.screen == tuiScreenSettings {
			m.toolbarHover = tuiToolbarHoverNone
			m.lastToolbarRow = -1
			return m.updateSettingsMouse(msg)
		}
		m.syncToolbarHover(msg)
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
	inStr := "in:" + inputModeLabel()

	pausedMu.RLock()
	isPaused := paused
	pausedMu.RUnlock()
	pausedStr := "running"
	if isPaused {
		pausedStr = "paused"
	}

	title := fmt.Sprintf(" spank %s | pack=%s | %s | %s | cd=%dms | v%s · o settings ",
		m.tuningLabel, m.packName, inStr, pausedStr, cd, version)
	if len(title) > iw+8 {
		title = fmt.Sprintf(" %s %s | %s | cd=%dms | o=settings ", m.packName, inStr, pausedStr, cd)
	}
	title = ansi.Truncate(title, iw, "…")

	eventLines := m.visibleEventLines(iw)

	scrollHint := ""
	if mx := m.maxScrollBack(); mx > 0 {
		scrollHint = fmt.Sprintf(" scroll %d/%d ", m.scrollBack, mx)
	}
	toolbar := m.renderToolbar()
	if lipgloss.Width(toolbar) > iw {
		toolbar = ansi.Truncate(toolbar, iw, "…")
	}
	meta := m.mainMouseDebugPrefix() + "o settings · input 3-way + file log there · p r · space pause · wheel scroll" + scrollHint
	if h := strings.TrimSpace(m.hintLine); h != "" {
		meta = h + " · " + meta
	}
	metaLine := lipgloss.NewStyle().Faint(true).Render(ansi.Truncate(meta, iw, "…"))

	border := lipgloss.RoundedBorder()
	box := lipgloss.NewStyle().
		Border(border).
		BorderForeground(lipgloss.Color("62")).
		Padding(0, 1).
		Width(iw + 2)

	blocks := []string{
		toolbar,
		lipgloss.NewStyle().Bold(true).Render(title),
		strings.Join(eventLines, "\n"),
		metaLine,
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
		linePack = fmt.Sprintf("%sSound pack        %s   ←/→ cycle built-ins", mark(tuiSetPack), m.packName)
	}
	lineCD := fmt.Sprintf("%sCooldown (ms)     %d   ←/→ or [ ]  ±%d", mark(tuiSetCooldown), cd, cooldownStep)
	lineSp := fmt.Sprintf("%sSpeed (×)         %.2f  , . fine  (%.2f–%.2f)", mark(tuiSetSpeed), sp, minSpeed, maxSpeed)
	lineVol := fmt.Sprintf("%sVolume × hold     %s   (v) toggle", mark(tuiSetVolume), vol)
	lineIn := fmt.Sprintf("%sListen input       %s   ←/→", mark(tuiSetInputMode), inputModeLabel())
	logStr := "off"
	if logToFile {
		logStr = "on"
	}
	lineLog := fmt.Sprintf("%sFile log           %s   spank.json + daily .log · ←/→", mark(tuiSetLog), logStr)

	linePack = ansi.Truncate(linePack, iw, "…")
	lineCD = ansi.Truncate(lineCD, iw, "…")
	lineSp = ansi.Truncate(lineSp, iw, "…")
	lineVol = ansi.Truncate(lineVol, iw, "…")
	lineIn = ansi.Truncate(lineIn, iw, "…")
	lineLog = ansi.Truncate(lineLog, iw, "…")

	title := lipgloss.NewStyle().Bold(true).Render(ansi.Truncate(" Settings · sound/input · esc/o back · q quit ", iw, "…"))
	hintStr := "↑↓/tab · ←→ / wheel · space: pack/vol/input/log · need an input mode"
	if m.mouseSeen {
		hintStr = fmt.Sprintf("ptr(%d,%d) · ", m.mouseX, m.mouseY) + hintStr
	}
	hint := lipgloss.NewStyle().Faint(true).Render(ansi.Truncate(hintStr, iw, "…"))

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
		lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(lineIn),
		lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(lineLog),
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
	cleanupIO, platOpts := bubbleTeaPlatformOptions()
	defer cleanupIO()
	opts := []tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	}
	opts = append(opts, platOpts...)
	p := tea.NewProgram(m, opts...)
	_, err := p.Run()
	if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return err
	}
	fmt.Fprintln(os.Stderr, "bye!")
	return nil
}
