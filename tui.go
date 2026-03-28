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

	tuiScreenMain int = iota
	tuiScreenSettings
)

const (
	tuiSetPack = iota
	tuiSetCooldown
	tuiSetSpeed
	tuiSetVolume
	tuiSetListenMouse
	tuiSetListenKeyboard
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

func inputListenSnapshot() (mouseOn, kbOn bool) {
	inputListenMu.RLock()
	defer inputListenMu.RUnlock()
	return listenMouse, listenKeyboard
}

func setListenMouse(on bool) {
	inputListenMu.Lock()
	defer inputListenMu.Unlock()
	if !on && !listenKeyboard {
		logFileLine("tui: at least one of mouse or keyboard must stay on")
		return
	}
	listenMouse = on
	logFileLine(fmt.Sprintf("tui: listen_mouse=%v", on))
}

func setListenKeyboard(on bool) {
	inputListenMu.Lock()
	defer inputListenMu.Unlock()
	if !on && !listenMouse {
		logFileLine("tui: at least one of mouse or keyboard must stay on")
		return
	}
	listenKeyboard = on
	logFileLine(fmt.Sprintf("tui: listen_keyboard=%v", on))
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

// Main panel layout: bubbletea MouseMsg Y is 0-based (top line of terminal = 0).
// viewMain: top border, title, tuiEventHeight event lines, footer buttons, meta, bottom border.
func (m *tuiModel) layoutMainRows0Based() (titleY, eventLastY, footerBtnY, innerLastY int) {
	titleY = 1
	eventLastY = titleY + tuiEventHeight // last event row
	footerBtnY = eventLastY + 1
	innerLastY = footerBtnY + 1
	return titleY, eventLastY, footerBtnY, innerLastY
}

// Settings panel: border, title, pack + sound rows + input rows, hint (same 0-based Y as MouseMsg).
func (m *tuiModel) layoutSettingsRows0Based() (titleY, rowPack, rowCD, rowSp, rowVol, rowListenM, rowListenK, hintY int) {
	titleY = 1
	rowPack = 2
	rowCD = 3
	rowSp = 4
	rowVol = 5
	rowListenM = 6
	rowListenK = 7
	hintY = 8
	return titleY, rowPack, rowCD, rowSp, rowVol, rowListenM, rowListenK, hintY
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

func (m *tuiModel) handleMainFooterClick(x int) tea.Cmd {
	tw := m.termW
	if tw < 3 {
		tw = max(3, m.innerWidth()+4)
	}
	// Full-width thirds: X is 0-based terminal column (same as bubbletea MouseMsg).
	f := float64(x) / float64(tw)
	switch {
	case f < 1.0/3:
		pausedMu.Lock()
		paused = true
		pausedMu.Unlock()
		logFileLine("tui: paused (mouse)")
	case f < 2.0/3:
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
	case tuiSetListenMouse:
		if dir > 0 {
			setListenMouse(true)
		} else if dir < 0 {
			setListenMouse(false)
		}
	case tuiSetListenKeyboard:
		if dir > 0 {
			setListenKeyboard(true)
		} else if dir < 0 {
			setListenKeyboard(false)
		}
	}
}

func (m *tuiModel) updateSettingsMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	_, rowPack, rowCD, rowSp, rowVol, rowListenM, rowListenK, hintY := m.layoutSettingsRows0Based()
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
		case rowListenM:
			m.settingsSel = tuiSetListenMouse
		case rowListenK:
			m.settingsSel = tuiSetListenKeyboard
		}
		return m, nil
	default:
		return m, nil
	}
}

func (m *tuiModel) updateMainMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	titleY, _, footerBtnY, innerLastY := m.layoutMainRows0Based()
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
		case tuiSetListenMouse:
			mOn, _ := inputListenSnapshot()
			setListenMouse(!mOn)
		case tuiSetListenKeyboard:
			_, kOn := inputListenSnapshot()
			setListenKeyboard(!kOn)
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
	mOn, kOn := inputListenSnapshot()
	inStr := "M:Y"
	if !mOn {
		inStr = "M:N"
	}
	if kOn {
		inStr += " K:Y"
	} else {
		inStr += " K:N"
	}

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
	footerLeft := lipgloss.NewStyle().Reverse(true).Padding(0, 1).Render(" Pause ")
	footerMid := lipgloss.NewStyle().Reverse(true).Padding(0, 1).Render(" Resume ")
	footerRight := lipgloss.NewStyle().Reverse(true).Padding(0, 1).Render(" Quit ")
	footer := lipgloss.JoinHorizontal(lipgloss.Top, footerLeft, " ", footerMid, " ", footerRight)
	footer = ansi.Truncate(footer, iw, "…")
	meta := "o settings · mouse/keyboard toggles there · p r · space pause · wheel scroll · K listens Space/Enter" + scrollHint
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
		linePack = fmt.Sprintf("%sSound pack        %s   ←/→ cycle built-ins", mark(tuiSetPack), m.packName)
	}
	lineCD := fmt.Sprintf("%sCooldown (ms)     %d   ←/→ or [ ]  ±%d", mark(tuiSetCooldown), cd, cooldownStep)
	lineSp := fmt.Sprintf("%sSpeed (×)         %.2f  , . fine  (%.2f–%.2f)", mark(tuiSetSpeed), sp, minSpeed, maxSpeed)
	lineVol := fmt.Sprintf("%sVolume × hold     %s   (v) toggle", mark(tuiSetVolume), vol)
	mOn, kOn := inputListenSnapshot()
	mStr, kStr := "off", "off"
	if mOn {
		mStr = "on"
	}
	if kOn {
		kStr = "on"
	}
	lineLM := fmt.Sprintf("%sListen mouse      %s   ←/→", mark(tuiSetListenMouse), mStr)
	lineLK := fmt.Sprintf("%sListen keyboard   %s   Space/Enter · ←/→", mark(tuiSetListenKeyboard), kStr)

	linePack = ansi.Truncate(linePack, iw, "…")
	lineCD = ansi.Truncate(lineCD, iw, "…")
	lineSp = ansi.Truncate(lineSp, iw, "…")
	lineVol = ansi.Truncate(lineVol, iw, "…")
	lineLM = ansi.Truncate(lineLM, iw, "…")
	lineLK = ansi.Truncate(lineLK, iw, "…")

	title := lipgloss.NewStyle().Bold(true).Render(ansi.Truncate(" Settings · sound/input · esc/o back · q quit ", iw, "…"))
	hint := lipgloss.NewStyle().Faint(true).Render(ansi.Truncate(
		"↑↓/tab · ←→ / wheel · space: pack/vol/listen toggle · need M or K on", iw, "…"))

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
		lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(lineLM),
		lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(lineLK),
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
