package logging

import (
	"log"
	"log/syslog"
	"strings"
)

type syslogWriter struct {
	write func(string) error
}

func (w syslogWriter) Write(p []byte) (int, error) {
	if err := w.write(strings.TrimRight(string(p), "\n")); err != nil {
		return 0, err
	}
	return len(p), nil
}

// UseSyslog routes each level to its own syslog priority. Syslog already
// records the level and time, so no prefix or timestamp is added.
func UseSyslog(tag string) error {
	w, err := syslog.New(syslog.LOG_DAEMON|syslog.LOG_INFO, tag)
	if err != nil {
		return err
	}
	mu.Lock()
	debugLogger = log.New(syslogWriter{w.Debug}, "", 0)
	infoLogger = log.New(syslogWriter{w.Info}, "", 0)
	warnLogger = log.New(syslogWriter{w.Warning}, "", 0)
	errorLogger = log.New(syslogWriter{w.Err}, "", 0)
	mu.Unlock()
	return nil
}
