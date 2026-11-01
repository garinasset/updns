package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

type Level int

const (
	DebugLevel Level = iota
	InfoLevel
	WarnLevel
	ErrorLevel
)

var (
	mu          sync.RWMutex
	level       = InfoLevel
	infoLogger  = log.New(os.Stdout, "INFO ", log.LstdFlags)
	warnLogger  = log.New(os.Stderr, "WARN ", log.LstdFlags)
	errorLogger = log.New(os.Stderr, "ERROR ", log.LstdFlags)
)

func SetLevel(value string) error {
	var parsed Level
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		parsed = DebugLevel
	case "info":
		parsed = InfoLevel
	case "warn", "warning":
		parsed = WarnLevel
	case "error":
		parsed = ErrorLevel
	default:
		return fmt.Errorf("unsupported log level %q", value)
	}

	mu.Lock()
	level = parsed
	mu.Unlock()
	return nil
}

// DebugEnabled lets hot paths skip building debug-only arguments.
func DebugEnabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return level <= DebugLevel
}

func Debugf(format string, args ...any) {
	write(DebugLevel, infoLogger, format, args...)
}

func Infof(format string, args ...any) {
	write(InfoLevel, infoLogger, format, args...)
}

func Warnf(format string, args ...any) {
	write(WarnLevel, warnLogger, format, args...)
}

func Errorf(format string, args ...any) {
	write(ErrorLevel, errorLogger, format, args...)
}

func write(messageLevel Level, output *log.Logger, format string, args ...any) {
	mu.RLock()
	defer mu.RUnlock()

	if messageLevel >= level {
		output.Printf(format, args...)
	}
}

func setOutputs(info, warning, failure io.Writer) {
	mu.Lock()
	infoLogger = log.New(info, "INFO ", log.LstdFlags)
	warnLogger = log.New(warning, "WARN ", log.LstdFlags)
	errorLogger = log.New(failure, "ERROR ", log.LstdFlags)
	mu.Unlock()
}
