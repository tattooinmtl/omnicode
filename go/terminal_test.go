//go:build windows

package main

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestTermEcho(t *testing.T) {
	dir := `C:\Users\ThePa`
	s, err := startTerm(dir, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer s.shutdown()
	deadline := time.Now().Add(8 * time.Second)
	nextWrite := time.Now().Add(400 * time.Millisecond)
	var got string
	for time.Now().Before(deadline) {
		if time.Now().After(nextWrite) {
			if err := writeHandle(s.in, []byte("echo HELLO-TERM\r")); err != nil {
				t.Fatal(err)
			}
			nextWrite = time.Now().Add(1500 * time.Millisecond)
		}
		s.mu.Lock()
		got = string(s.history)
		s.mu.Unlock()
		if strings.Count(got, "HELLO-TERM") >= 2 {
			t.Logf("output:\n%q", got)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	var code uint32
	_ = windows.GetExitCodeProcess(s.proc, &code)
	s.mu.Lock()
	errText := s.lastErr
	s.mu.Unlock()
	t.Fatalf("history %q exit=%d readErr=%s", got, code, errText)
}
