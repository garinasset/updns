package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestLogLevelFiltersAndRoutesMessages(t *testing.T) {
	var info, warning, failure bytes.Buffer
	mu.RLock()
	oldInfo, oldWarning, oldError, oldLevel := infoLogger, warnLogger, errorLogger, level
	mu.RUnlock()
	setOutputs(&info, &warning, &failure)
	t.Cleanup(func() {
		mu.Lock()
		infoLogger, warnLogger, errorLogger, level = oldInfo, oldWarning, oldError, oldLevel
		mu.Unlock()
	})

	if err := SetLevel("warn"); err != nil {
		t.Fatal(err)
	}

	Debugf("debug message")
	Infof("info message")
	Warnf("warning message")
	Errorf("error message")

	if got := info.String(); got != "" {
		t.Errorf("info output = %q, want empty at warn level", got)
	}
	if got := warning.String(); !strings.Contains(got, "warning message") {
		t.Errorf("warning output = %q, want warning message", got)
	}
	if got := failure.String(); !strings.Contains(got, "error message") {
		t.Errorf("error output = %q, want error message", got)
	}
}

func TestSetLevelRejectsUnknownValue(t *testing.T) {
	if err := SetLevel("verbose"); err == nil {
		t.Fatal("SetLevel(verbose) succeeded, want error")
	}
}
