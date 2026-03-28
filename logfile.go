package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// rotatingDailyLogger appends timestamped lines to spank-YYYY-MM-DD.log under dir
// and removes log files older than retentionDays (by date in filename).
type rotatingDailyLogger struct {
	mu         sync.Mutex
	dir        string
	retention  int
	file       *os.File
	currentDay string
	now        func() time.Time
}

var spankLogNameRe = regexp.MustCompile(`^spank-(\d{4}-\d{2}-\d{2})\.log$`)

// fileLogger is nil when file logging is off. Guard with fileLoggerMu for enable/disable.
var (
	fileLogger      *rotatingDailyLogger
	fileLoggerMu    sync.RWMutex
	logPruneCancel  context.CancelFunc
)

func enableFileLogging(parent context.Context) error {
	fileLoggerMu.Lock()
	defer fileLoggerMu.Unlock()
	if fileLogger != nil {
		return nil
	}
	lg, err := newRotatingDailyLogger(logDir, logRetention)
	if err != nil {
		return err
	}
	fileLogger = lg
	pruneCtx, cancel := context.WithCancel(parent)
	logPruneCancel = cancel
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-pruneCtx.Done():
				return
			case <-ticker.C:
				fileLoggerMu.Lock()
				if fileLogger != nil {
					fileLogger.Prune()
				}
				fileLoggerMu.Unlock()
			}
		}
	}()
	return nil
}

func disableFileLogging() {
	fileLoggerMu.Lock()
	defer fileLoggerMu.Unlock()
	if logPruneCancel != nil {
		logPruneCancel()
		logPruneCancel = nil
	}
	if fileLogger != nil {
		_ = fileLogger.Close()
		fileLogger = nil
	}
}

func newRotatingDailyLogger(dir string, retentionDays int) (*rotatingDailyLogger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("log dir: %w", err)
	}
	l := &rotatingDailyLogger{
		dir:       dir,
		retention: retentionDays,
		now:       time.Now,
	}
	l.pruneOldest()
	return l, nil
}

func (l *rotatingDailyLogger) setNow(f func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = f
}

// Close releases the open log file.
func (l *rotatingDailyLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		l.currentDay = ""
		return err
	}
	return nil
}

// WriteLine appends one line with RFC3339 local timestamp prefix.
func (l *rotatingDailyLogger) WriteLine(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	day := now.Format("2006-01-02")
	if l.file == nil || l.currentDay != day {
		if l.file != nil {
			_ = l.file.Close()
			l.file = nil
		}
		path := filepath.Join(l.dir, fmt.Sprintf("spank-%s.log", day))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		l.file = f
		l.currentDay = day
	}
	line := now.Format(time.RFC3339) + " " + msg + "\n"
	_, _ = l.file.WriteString(line)
}

// Prune removes log files older than retention (by filename date). Safe to call periodically.
func (l *rotatingDailyLogger) Prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneOldest()
}

func (l *rotatingDailyLogger) pruneOldest() {
	cutoff := l.now().AddDate(0, 0, -l.retention).Truncate(24 * time.Hour)
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := spankLogNameRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02", m[1], time.Local)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			_ = os.Remove(filepath.Join(l.dir, e.Name()))
		}
	}
}

func logFileLine(msg string) {
	fileLoggerMu.RLock()
	lg := fileLogger
	fileLoggerMu.RUnlock()
	if lg != nil {
		lg.WriteLine(msg)
	}
}
