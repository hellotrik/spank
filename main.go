// spank plays audio when you release the left mouse button (hold duration
// maps to volume when using --volume-scaling). macOS and Windows only.
package main

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/fang"
	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/effects"
	"github.com/gopxl/beep/v2/mp3"
	"github.com/gopxl/beep/v2/speaker"
	"github.com/spf13/cobra"
)

var version = "dev"

//go:embed audio/pain/*.mp3
var painAudio embed.FS

//go:embed audio/sexy/*.mp3
var sexyAudio embed.FS

//go:embed audio/halo/*.mp3
var haloAudio embed.FS

//go:embed audio/lizard/*.mp3
var lizardAudio embed.FS

//go:embed audio/sward/*.mp3
var swardAudio embed.FS

var (
	sexyMode      bool
	haloMode      bool
	lizardMode    bool
	swardMode     bool
	customPath    string
	customFiles   []string
	fastMode      bool
	cooldownMs    int
	stdioMode     bool
	volumeScaling bool
	paused        bool
	pausedMu      sync.RWMutex
	speedRatio    float64
	plainOutput   bool // --no-tui / --plain: disable window TUI
	logToFile     bool
	logDir        string
	logRetention  int

	// soundMu protects cooldownMs, speedRatio, and volumeScaling (TUI, stdin, audio).
	soundMu sync.RWMutex

	// runtimeCustomPack is true when the user started with --custom/--custom-files; TUI cannot switch embedded packs.
	runtimeCustomPack bool

	// inputListenMu protects listenMouse and listenKeyboard.
	inputListenMu  sync.RWMutex
	listenMouse    = true
	listenKeyboard = false
)

// mouseHoldLibErrOnce logs a single failure from CoreGraphics mouse APIs.
var mouseHoldLibErrOnce sync.Once

// keyboardHoldLibErrOnce logs a single failure from keyboard state APIs.
var keyboardHoldLibErrOnce sync.Once

type mouseHoldState struct {
	down   bool
	downAt time.Time
}

// keyboardHoldState tracks a single key hold/release (same shape as mouseHoldState).
type keyboardHoldState = mouseHoldState

type playMode int

const (
	modeRandom playMode = iota
	modeEscalation
)

const (
	// decayHalfLife is how many seconds of inactivity before intensity
	// halves. Controls how fast escalation fades.
	decayHalfLife = 30.0

	// defaultMinAmplitude is the default detection threshold.
	defaultMinAmplitude = 0.05

	// defaultCooldownMs is the default cooldown between audio responses.
	defaultCooldownMs = 750

	// defaultSpeedRatio is the default playback speed (1.0 = normal).
	defaultSpeedRatio = 1.0

	// defaultMousePollInterval is how often we sample the left mouse button.
	defaultMousePollInterval = 10 * time.Millisecond
)

type runtimeTuning struct {
	cooldown     time.Duration
	pollInterval time.Duration
}

func defaultTuning() runtimeTuning {
	return runtimeTuning{
		cooldown:     time.Duration(defaultCooldownMs) * time.Millisecond,
		pollInterval: defaultMousePollInterval,
	}
}

func applyFastOverlay(base runtimeTuning) runtimeTuning {
	base.pollInterval = 4 * time.Millisecond
	base.cooldown = 350 * time.Millisecond
	return base
}

type soundPack struct {
	name   string
	fs     embed.FS
	dir    string
	mode   playMode
	files  []string
	custom bool
}

func (sp *soundPack) loadFiles() error {
	if sp.custom {
		entries, err := os.ReadDir(sp.dir)
		if err != nil {
			return err
		}
		sp.files = make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() {
				sp.files = append(sp.files, sp.dir+"/"+entry.Name())
			}
		}
	} else {
		entries, err := sp.fs.ReadDir(sp.dir)
		if err != nil {
			return err
		}
		sp.files = make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() {
				sp.files = append(sp.files, sp.dir+"/"+entry.Name())
			}
		}
	}
	sort.Strings(sp.files)
	if len(sp.files) == 0 {
		return fmt.Errorf("no audio files found in %s", sp.dir)
	}
	return nil
}

// loadEmbeddedPackByID loads a built-in pack by name (pain, sexy, halo, lizard, sward).
func loadEmbeddedPackByID(id string) (*soundPack, error) {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "pain":
		p := &soundPack{name: "pain", fs: painAudio, dir: "audio/pain", mode: modeRandom}
		if err := p.loadFiles(); err != nil {
			return nil, err
		}
		return p, nil
	case "sexy":
		p := &soundPack{name: "sexy", fs: sexyAudio, dir: "audio/sexy", mode: modeEscalation}
		if err := p.loadFiles(); err != nil {
			return nil, err
		}
		return p, nil
	case "halo":
		p := &soundPack{name: "halo", fs: haloAudio, dir: "audio/halo", mode: modeRandom}
		if err := p.loadFiles(); err != nil {
			return nil, err
		}
		return p, nil
	case "lizard":
		p := &soundPack{name: "lizard", fs: lizardAudio, dir: "audio/lizard", mode: modeEscalation}
		if err := p.loadFiles(); err != nil {
			return nil, err
		}
		return p, nil
	case "sward":
		p := &soundPack{name: "sward", fs: swardAudio, dir: "audio/sward", mode: modeRandom}
		if err := p.loadFiles(); err != nil {
			return nil, err
		}
		return p, nil
	default:
		return nil, fmt.Errorf("unknown embedded pack %q", id)
	}
}

type slapTracker struct {
	mu       sync.Mutex
	score    float64
	lastTime time.Time
	total    int
	halfLife float64 // seconds
	scale    float64 // controls the escalation curve shape
	pack     *soundPack
}

func newSlapTracker(pack *soundPack, cooldown time.Duration) *slapTracker {
	// scale maps the exponential curve so that sustained max-rate
	// slapping (one per cooldown) reaches the final file. At steady
	// state the score converges to ssMax; we set scale so that score
	// maps to the last index.
	cooldownSec := cooldown.Seconds()
	ssMax := 1.0 / (1.0 - math.Pow(0.5, cooldownSec/decayHalfLife))
	scale := (ssMax - 1) / math.Log(float64(len(pack.files)+1))
	return &slapTracker{
		halfLife: decayHalfLife,
		scale:    scale,
		pack:     pack,
	}
}

func (st *slapTracker) record(now time.Time) (int, float64) {
	st.mu.Lock()
	defer st.mu.Unlock()

	if !st.lastTime.IsZero() {
		elapsed := now.Sub(st.lastTime).Seconds()
		st.score *= math.Pow(0.5, elapsed/st.halfLife)
	}
	st.score += 1.0
	st.lastTime = now
	st.total++
	return st.total, st.score
}

func (st *slapTracker) getFile(score float64) string {
	if st.pack.mode == modeRandom {
		return st.pack.files[rand.Intn(len(st.pack.files))]
	}

	// Escalation: 1-exp(-x) curve maps score to file index.
	// At sustained max slap rate, score reaches ssMax which maps
	// to the final file.
	maxIdx := len(st.pack.files) - 1
	idx := min(int(float64(len(st.pack.files))*(1.0-math.Exp(-(score-1)/st.scale))), maxIdx)
	return st.pack.files[idx]
}

// mouseHoldMinPlay is ignores very short releases (noise / accidental clicks).
const mouseHoldMinPlay = 30 * time.Millisecond

// mouseHoldDurationToAmplitude maps hold time to a pseudo slap amplitude for
// playback and --volume-scaling (longer hold -> louder, capped).
func mouseHoldDurationToAmplitude(d time.Duration) float64 {
	ms := float64(d) / float64(time.Millisecond)
	const maxMs = 2500.0
	if ms > maxMs {
		ms = maxMs
	}
	t := ms / maxMs
	return defaultMinAmplitude + t*(0.75-defaultMinAmplitude)
}

func updateMouseLeftHold(s *mouseHoldState, now time.Time) (released bool, relTime time.Time, hold time.Duration) {
	down, err := leftMouseButtonDown()
	if err != nil {
		mouseHoldLibErrOnce.Do(func() {
			msg := fmt.Sprintf("spank: mouse: %v", err)
			fmt.Fprintln(os.Stderr, msg)
			logFileLine("ERROR " + msg)
		})
		return false, time.Time{}, 0
	}
	switch {
	case down && !s.down:
		s.down = true
		s.downAt = now
	case !down && s.down:
		s.down = false
		return true, now, now.Sub(s.downAt)
	}
	return false, time.Time{}, 0
}

func updateKeyHold(s *keyboardHoldState, now time.Time, downFn func() (bool, error), label string) (released bool, relTime time.Time, hold time.Duration) {
	down, err := downFn()
	if err != nil {
		keyboardHoldLibErrOnce.Do(func() {
			msg := fmt.Sprintf("spank: keyboard (%s): %v", label, err)
			fmt.Fprintln(os.Stderr, msg)
			logFileLine("ERROR " + msg)
		})
		return false, time.Time{}, 0
	}
	switch {
	case down && !s.down:
		s.down = true
		s.downAt = now
	case !down && s.down:
		s.down = false
		return true, now, now.Sub(s.downAt)
	}
	return false, time.Time{}, 0
}

func updateSpaceKeyHold(s *keyboardHoldState, now time.Time) (released bool, relTime time.Time, hold time.Duration) {
	return updateKeyHold(s, now, spaceKeyDown, "Space")
}

func updateEnterKeyHold(s *keyboardHoldState, now time.Time) (released bool, relTime time.Time, hold time.Duration) {
	return updateKeyHold(s, now, enterKeyDown, "Enter")
}

func main() {
	cmd := &cobra.Command{
		Use:   "spank",
		Short: "Plays audio when you release the left mouse button",
		Long: `spank watches the left mouse button; when you release it, it plays a clip
from the selected sound pack (hold duration affects volume with --volume-scaling).

Use --sexy for escalation: the more often you trigger within a window, the more
intense the sounds become.

Use --halo for random Halo clips on each release.

Use --lizard for lizard-style escalation like --sexy.

Use --sward for the sward sound pack.

Use --keyboard to also listen for Space and Enter key hold/release (independent of --mouse; both can be toggled in the TUI).`,
		Version: version,
		RunE: func(cmd *cobra.Command, args []string) error {
			tuning := defaultTuning()
			if fastMode {
				tuning = applyFastOverlay(tuning)
			}
			if cmd.Flags().Changed("cooldown") {
				tuning.cooldown = time.Duration(cooldownMs) * time.Millisecond
			}
			return run(cmd.Context(), tuning)
		},
		SilenceUsage: true,
	}

	cmd.Flags().BoolVarP(&sexyMode, "sexy", "s", false, "Enable sexy mode")
	cmd.Flags().BoolVarP(&haloMode, "halo", "H", false, "Enable halo mode")
	cmd.Flags().BoolVarP(&lizardMode, "lizard", "l", false, "Enable lizard mode (escalating intensity)")
	cmd.Flags().BoolVar(&swardMode, "sward", false, "Enable sward sound pack")
	cmd.Flags().StringVarP(&customPath, "custom", "c", "", "Path to custom MP3 audio directory")
	cmd.Flags().BoolVar(&fastMode, "fast", false, "Shorter mouse poll interval and cooldown")
	cmd.Flags().StringSliceVar(&customFiles, "custom-files", nil, "Comma-separated list of custom MP3 files")
	cmd.Flags().IntVar(&cooldownMs, "cooldown", defaultCooldownMs, "Cooldown between responses in milliseconds")
	cmd.Flags().BoolVar(&stdioMode, "stdio", false, "Enable stdio mode: JSON output and stdin commands (for GUI integration)")
	cmd.Flags().BoolVar(&plainOutput, "no-tui", false, "Disable window TUI; print events as plain text (default on macOS/Windows is TUI)")
	cmd.Flags().BoolVar(&plainOutput, "plain", false, "Same as --no-tui")
	cmd.Flags().BoolVar(&logToFile, "log", false, "Append events to daily rotating log files under --log-dir")
	cmd.Flags().StringVar(&logDir, "log-dir", ".", "Directory for log files when --log is set")
	cmd.Flags().IntVar(&logRetention, "log-retention-days", 7, "Delete log files older than this many days (by date in filename)")
	cmd.Flags().BoolVar(&volumeScaling, "volume-scaling", false, "Scale playback volume by hold duration (longer hold = louder)")
	cmd.Flags().Float64Var(&speedRatio, "speed", defaultSpeedRatio, "Playback speed multiplier (0.5 = half speed, 2.0 = double speed)")
	cmd.Flags().BoolVar(&listenMouse, "mouse", true, "Listen to left mouse button hold/release")
	cmd.Flags().BoolVar(&listenKeyboard, "keyboard", false, "Listen to Space/Enter key hold/release (global; may overlap TUI keys)")

	if err := fang.Execute(context.Background(), cmd); err != nil {
		os.Exit(1)
	}
}

func run(ctx context.Context, tuning runtimeTuning) error {
	modeCount := 0
	if sexyMode {
		modeCount++
	}
	if haloMode {
		modeCount++
	}
	if lizardMode {
		modeCount++
	}
	if swardMode {
		modeCount++
	}
	if customPath != "" || len(customFiles) > 0 {
		modeCount++
	}
	if modeCount > 1 {
		return fmt.Errorf("--sexy, --halo, --lizard, --sward, and --custom/--custom-files are mutually exclusive; pick one")
	}

	if tuning.cooldown <= 0 {
		return fmt.Errorf("--cooldown must be greater than 0")
	}

	if logRetention < 1 {
		return fmt.Errorf("--log-retention-days must be at least 1")
	}

	if !listenMouse && !listenKeyboard {
		return fmt.Errorf("at least one of --mouse or --keyboard must be enabled")
	}

	useWindowTUI = !stdioMode && !plainOutput
	if stdioMode {
		useWindowTUI = false
	}

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var pack *soundPack
	switch {
	case len(customFiles) > 0:
		// Validate all files exist and are MP3s
		for _, f := range customFiles {
			if !strings.HasSuffix(strings.ToLower(f), ".mp3") {
				return fmt.Errorf("custom file must be MP3: %s", f)
			}
			if _, err := os.Stat(f); err != nil {
				return fmt.Errorf("custom file not found: %s", f)
			}
		}
		pack = &soundPack{name: "custom", mode: modeRandom, custom: true, files: customFiles}
	case customPath != "":
		pack = &soundPack{name: "custom", dir: customPath, mode: modeRandom, custom: true}
	case sexyMode:
		pack = &soundPack{name: "sexy", fs: sexyAudio, dir: "audio/sexy", mode: modeEscalation}
	case haloMode:
		pack = &soundPack{name: "halo", fs: haloAudio, dir: "audio/halo", mode: modeRandom}
	case lizardMode:
		pack = &soundPack{name: "lizard", fs: lizardAudio, dir: "audio/lizard", mode: modeEscalation}
	case swardMode:
		pack = &soundPack{name: "sward", fs: swardAudio, dir: "audio/sward", mode: modeRandom}
	default:
		pack = &soundPack{name: "pain", fs: painAudio, dir: "audio/pain", mode: modeRandom}
	}

	// Only load files if not already set (customFiles case)
	if len(pack.files) == 0 {
		if err := pack.loadFiles(); err != nil {
			return fmt.Errorf("loading %s audio: %w", pack.name, err)
		}
	}
	runtimeCustomPack = pack.custom

	if logToFile {
		lg, err := newRotatingDailyLogger(logDir, logRetention)
		if err != nil {
			return fmt.Errorf("file log: %w", err)
		}
		fileLogger = lg
		defer func() {
			_ = fileLogger.Close()
			fileLogger = nil
		}()
		go func() {
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					fileLogger.Prune()
				}
			}
		}()
		logFileLine(fmt.Sprintf("spank start pack=%s version=%s stdio=%v plain=%v tui=%v", pack.name, version, stdioMode, plainOutput, useWindowTUI))
	}

	return platformRun(ctx, tuning, pack)
}

var (
	speakerMu       sync.Mutex
	speakerInitFail bool // permanent: Init failed (e.g. root has no audio session on macOS)

	audioWorkerOnce sync.Once
	audioJobCh      chan audioJob
)

// audioJob queues work for a single goroutine so oto/speaker/mixer are never
// used concurrently (avoids hangs on Windows); callers stay non-blocking.
type audioJob struct {
	pack        *soundPack
	path        string
	amplitude   float64
	speakerInit *bool
}

func ensureAudioWorker() {
	audioWorkerOnce.Do(func() {
		audioJobCh = make(chan audioJob, 32)
		go func() {
			for j := range audioJobCh {
				playAudioSync(j.pack, j.path, j.amplitude, j.speakerInit)
			}
		}()
	})
}

// amplitudeToVolume maps a detected amplitude to a beep/effects.Volume
// level. Amplitude typically ranges from ~0.05 (light tap) to ~1.0+
// (hard slap). The mapping uses a logarithmic curve so that light taps
// are noticeably quieter and hard hits play near full volume.
//
// Returns a value in the range [-3.0, 0.0] for use with effects.Volume
// (base 2): -3.0 is ~1/8 volume, 0.0 is full volume.
func amplitudeToVolume(amplitude float64) float64 {
	const (
		minAmp = 0.05 // softest detectable
		maxAmp = 0.80 // treat anything above this as max
		minVol = -3.0 // quietest playback (1/8 volume with base 2)
		maxVol = 0.0  // full volume
	)

	// Clamp
	if amplitude <= minAmp {
		return minVol
	}
	if amplitude >= maxAmp {
		return maxVol
	}

	// Normalize to [0, 1]
	t := (amplitude - minAmp) / (maxAmp - minAmp)

	// Log curve for more natural volume scaling
	// log(1 + t*99) / log(100) maps [0,1] -> [0,1] with a log curve
	t = math.Log(1+t*99) / math.Log(100)

	return minVol + t*(maxVol-minVol)
}

func playAudio(pack *soundPack, path string, amplitude float64, speakerInit *bool) {
	ensureAudioWorker()
	audioJobCh <- audioJob{pack: pack, path: path, amplitude: amplitude, speakerInit: speakerInit}
}

func playAudioSync(pack *soundPack, path string, amplitude float64, speakerInit *bool) {
	var streamer beep.StreamSeekCloser
	var format beep.Format

	if pack.custom {
		file, err := os.Open(path)
		if err != nil {
			msg := fmt.Sprintf("spank: open %s: %v", path, err)
			fmt.Fprintln(os.Stderr, msg)
			logFileLine("ERROR " + msg)
			return
		}
		defer file.Close()
		streamer, format, err = mp3.Decode(file)
		if err != nil {
			msg := fmt.Sprintf("spank: decode %s: %v", path, err)
			fmt.Fprintln(os.Stderr, msg)
			logFileLine("ERROR " + msg)
			return
		}
	} else {
		data, err := pack.fs.ReadFile(path)
		if err != nil {
			msg := fmt.Sprintf("spank: read %s: %v", path, err)
			fmt.Fprintln(os.Stderr, msg)
			logFileLine("ERROR " + msg)
			return
		}
		streamer, format, err = mp3.Decode(io.NopCloser(bytes.NewReader(data)))
		if err != nil {
			msg := fmt.Sprintf("spank: decode %s: %v", path, err)
			fmt.Fprintln(os.Stderr, msg)
			logFileLine("ERROR " + msg)
			return
		}
	}
	defer streamer.Close()

	bufSamples := format.SampleRate.N(time.Second / 10)
	if runtime.GOOS == "windows" {
		bufSamples = format.SampleRate.N(time.Second / 4)
	}

	speakerMu.Lock()
	if speakerInitFail {
		speakerMu.Unlock()
		return
	}
	if !*speakerInit {
		if err := speaker.Init(format.SampleRate, bufSamples); err != nil {
			speakerInitFail = true
			speakerMu.Unlock()
			msg := fmt.Sprintf("spank: speaker init failed (no playback): %v", err)
			fmt.Fprintln(os.Stderr, msg)
			logFileLine("ERROR " + msg)
			return
		}
		*speakerInit = true
	}
	speakerMu.Unlock()

	soundMu.RLock()
	volScale := volumeScaling
	spd := speedRatio
	soundMu.RUnlock()

	var source beep.Streamer = streamer
	if volScale {
		source = &effects.Volume{
			Streamer: streamer,
			Base:     2,
			Volume:   amplitudeToVolume(amplitude),
			Silent:   false,
		}
	}

	if spd != 1.0 && spd > 0 {
		fakeRate := beep.SampleRate(int(float64(format.SampleRate) * spd))
		source = beep.Resample(4, fakeRate, format.SampleRate, source)
	}

	done := make(chan bool)
	speaker.Play(beep.Seq(source, beep.Callback(func() {
		done <- true
	})))
	<-done
}

// stdinCommand represents a command received via stdin
type stdinCommand struct {
	Cmd      string  `json:"cmd"`
	Cooldown int     `json:"cooldown,omitempty"`
	Speed    float64 `json:"speed,omitempty"`
}

// readStdinCommands reads JSON commands from stdin for live control
func readStdinCommands() {
	processCommands(os.Stdin, os.Stdout)
}

// processCommands reads JSON commands from r and writes responses to w.
// This is the testable core of the stdin command handler.
func processCommands(r io.Reader, w io.Writer) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var cmd stdinCommand
		if err := json.Unmarshal([]byte(line), &cmd); err != nil {
			if stdioMode {
				fmt.Fprintf(w, `{"error":"invalid command: %s"}%s`, err.Error(), "\n")
			}
			continue
		}

		switch cmd.Cmd {
		case "pause":
			pausedMu.Lock()
			paused = true
			pausedMu.Unlock()
			if stdioMode {
				fmt.Fprintln(w, `{"status":"paused"}`)
			}
		case "resume":
			pausedMu.Lock()
			paused = false
			pausedMu.Unlock()
			if stdioMode {
				fmt.Fprintln(w, `{"status":"resumed"}`)
			}
		case "set":
			soundMu.Lock()
			if cmd.Cooldown > 0 {
				cooldownMs = cmd.Cooldown
			}
			if cmd.Speed > 0 {
				speedRatio = cmd.Speed
			}
			cd := cooldownMs
			sp := speedRatio
			soundMu.Unlock()
			if stdioMode {
				fmt.Fprintf(w, `{"status":"settings_updated","cooldown":%d,"speed":%.2f}%s`, cd, sp, "\n")
			}
		case "volume-scaling":
			soundMu.Lock()
			volumeScaling = !volumeScaling
			vs := volumeScaling
			soundMu.Unlock()
			if stdioMode {
				fmt.Fprintf(w, `{"status":"volume_scaling_toggled","volume_scaling":%t}%s`, vs, "\n")
			}
		case "status":
			pausedMu.RLock()
			isPaused := paused
			pausedMu.RUnlock()
			soundMu.RLock()
			cd := cooldownMs
			vs := volumeScaling
			sp := speedRatio
			soundMu.RUnlock()
			if stdioMode {
				fmt.Fprintf(w, `{"status":"ok","paused":%t,"cooldown":%d,"volume_scaling":%t,"speed":%.2f}%s`, isPaused, cd, vs, sp, "\n")
			}
		default:
			if stdioMode {
				fmt.Fprintf(w, `{"error":"unknown command: %s"}%s`, cmd.Cmd, "\n")
			}
		}
	}
}
