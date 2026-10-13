//go:build windows

package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	k32            = windows.NewLazySystemDLL("kernel32.dll")
	procInitAttr   = k32.NewProc("InitializeProcThreadAttributeList")
	procUpdateAttr = k32.NewProc("UpdateProcThreadAttribute")
	procDeleteAttr = k32.NewProc("DeleteProcThreadAttributeList")
)

// startupInfoEx is laid out so CreateProcess reads the attribute-list pointer
// as the first word after STARTUPINFO. A slice header's first word is that
// pointer, which is why the list is a []byte and not a separate field we pass
// by address.
type startupInfoEx struct {
	startupInfo   windows.StartupInfo
	attributeList []byte
}

// A terminal session is a real Windows console (ConPTY) running PowerShell
// in a project folder, so a CLI agent such as claude, kimi, or mmx can run
// inside the browser pane.

type termSession struct {
	id     string
	dir    string
	pc     windows.Handle
	in     windows.Handle
	out    windows.Handle
	ptyIn  windows.Handle
	ptyOut windows.Handle
	proc   windows.Handle

	mu      sync.Mutex
	history []byte
	subs    map[chan []byte]struct{}
	dead    bool
	lastErr string
}

var (
	termMu sync.Mutex
	terms  = map[string]*termSession{}
)

type termAgent struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Command string `json:"command"`
	Found   bool   `json:"found"`
}

func termCreateHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"project_id"`
		Cols      int    `json:"cols"`
		Rows      int    `json:"rows"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	dir := ""
	if strings.TrimSpace(req.ProjectID) != "" {
		dir = projectPath(req.ProjectID, "")
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		dir = home
	}
	cols, rows := clampTerm(req.Cols, req.Rows)
	s, err := startTerm(dir, cols, rows)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	termMu.Lock()
	terms[s.id] = s
	termMu.Unlock()
	writeOK(w, map[string]any{
		"id":     s.id,
		"cwd":    dir,
		"agents": termAgents(),
	})
}

func termStreamHandler(w http.ResponseWriter, r *http.Request) {
	s := lookupTerm(r.PathValue("id"))
	if s == nil {
		writeErr(w, 404, fmt.Errorf("terminal not found"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	ch, history := s.subscribe()
	defer s.unsubscribe(ch)
	writeTermChunk(w, flusher, history)
	for {
		select {
		case <-r.Context().Done():
			return
		case chunk, open := <-ch:
			if !open {
				fmt.Fprintf(w, "event: exit\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			writeTermChunk(w, flusher, chunk)
		}
	}
}

func termInputHandler(w http.ResponseWriter, r *http.Request) {
	s := lookupTerm(r.PathValue("id"))
	if s == nil {
		writeErr(w, 404, fmt.Errorf("terminal not found"))
		return
	}
	var req struct {
		Data string `json:"data"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.Data == "" {
		writeOK(w, map[string]bool{"ok": true})
		return
	}
	if err := writeHandle(s.in, []byte(req.Data)); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

func termResizeHandler(w http.ResponseWriter, r *http.Request) {
	s := lookupTerm(r.PathValue("id"))
	if s == nil {
		writeErr(w, 404, fmt.Errorf("terminal not found"))
		return
	}
	var req struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	cols, rows := clampTerm(req.Cols, req.Rows)
	if err := windows.ResizePseudoConsole(s.pc, windows.Coord{X: int16(cols), Y: int16(rows)}); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

func termCloseHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	termMu.Lock()
	s := terms[id]
	delete(terms, id)
	termMu.Unlock()
	if s != nil {
		s.shutdown()
	}
	writeOK(w, map[string]bool{"ok": true})
}

func lookupTerm(id string) *termSession {
	termMu.Lock()
	defer termMu.Unlock()
	return terms[id]
}

func clampTerm(cols, rows int) (int, int) {
	if cols < 20 {
		cols = 80
	}
	if rows < 5 {
		rows = 24
	}
	if cols > 300 {
		cols = 300
	}
	if rows > 80 {
		rows = 80
	}
	return cols, rows
}

func termAgents() []termAgent {
	specs := []termAgent{
		{ID: "claude", Label: "Claude", Command: "claude"},
		{ID: "kimi", Label: "Kimi", Command: "kimi"},
		{ID: "mmx", Label: "MiniMax", Command: "mmx"},
	}
	for i := range specs {
		specs[i].Found = findAgent(specs[i].Command) != ""
	}
	return specs
}

func extraBins() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".kimi-code", "bin"),
		filepath.Join(os.Getenv("APPDATA"), "npm"),
	}
}

func findAgent(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range extraBins() {
		for _, ext := range []string{"", ".exe", ".cmd", ".ps1"} {
			cand := filepath.Join(dir, name+ext)
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				return cand
			}
		}
	}
	return ""
}

func terminalEnv() []string {
	env := os.Environ()
	extras := extraBins()
	for i, entry := range env {
		eq := strings.IndexByte(entry, '=')
		if eq <= 0 || !strings.EqualFold(entry[:eq], "PATH") {
			continue
		}
		cur := entry[eq+1:]
		low := strings.ToLower(cur)
		for _, dir := range extras {
			if dir == "" || strings.Contains(low, strings.ToLower(dir)) {
				continue
			}
			cur = dir + string(os.PathListSeparator) + cur
		}
		env[i] = entry[:eq] + "=" + cur
		return env
	}
	return env
}

func closeHandles(hs ...windows.Handle) {
	for _, h := range hs {
		if h != 0 && h != windows.InvalidHandle {
			windows.CloseHandle(h)
		}
	}
}

func startTerm(dir string, cols, rows int) (sess *termSession, err error) {
	var ptyIn, inputWrite, outputRead, ptyOut, pc windows.Handle
	var attrReady bool
	var attrList []byte
	defer func() {
		if attrReady {
			procDeleteAttr.Call(uintptr(unsafe.Pointer(&attrList[0])))
			attrReady = false
		}
		if sess != nil {
			return
		}
		if pc != 0 {
			windows.ClosePseudoConsole(pc)
		}
		closeHandles(ptyIn, inputWrite, outputRead, ptyOut)
	}()

	if err = windows.CreatePipe(&ptyIn, &inputWrite, nil, 0); err != nil {
		return nil, err
	}
	if err = windows.CreatePipe(&outputRead, &ptyOut, nil, 0); err != nil {
		return nil, err
	}
	if err = windows.CreatePseudoConsole(windows.Coord{X: int16(cols), Y: int16(rows)}, ptyIn, ptyOut, 0, &pc); err != nil {
		return nil, fmt.Errorf("conpty: %w", err)
	}

	var size uintptr
	procInitAttr.Call(0, 1, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return nil, fmt.Errorf("InitializeProcThreadAttributeList returned no size")
	}
	attrList = make([]byte, size)
	r, _, callErr := procInitAttr.Call(uintptr(unsafe.Pointer(&attrList[0])), 1, 0, uintptr(unsafe.Pointer(&size)))
	if r != 1 {
		return nil, fmt.Errorf("InitializeProcThreadAttributeList: %v", callErr)
	}
	attrReady = true
	// lpValue is the HPCON itself. A pointer to the handle is an invalid
	// pseudoconsole and the child dies with 0xc0000142.
	r, _, callErr = procUpdateAttr.Call(
		uintptr(unsafe.Pointer(&attrList[0])),
		0,
		uintptr(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE),
		uintptr(pc),
		unsafe.Sizeof(pc),
		0,
		0,
	)
	if r != 1 {
		return nil, fmt.Errorf("UpdateProcThreadAttribute: %v", callErr)
	}

	system32, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	exe := filepath.Join(system32, "WindowsPowerShell", "v1.0", "powershell.exe")
	cmdLine, err := windows.UTF16PtrFromString(`"` + exe + `" -NoLogo -NoExit`)
	if err != nil {
		return nil, err
	}
	dirPtr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return nil, err
	}
	envBlock, err := utf16EnvBlock(terminalEnv())
	if err != nil {
		return nil, err
	}
	si := &startupInfoEx{}
	si.startupInfo.Cb = uint32(unsafe.Sizeof(windows.StartupInfo{}) + unsafe.Sizeof(uintptr(0)))
	si.startupInfo.Flags = windows.STARTF_USESTDHANDLES
	si.attributeList = attrList

	pi := new(windows.ProcessInformation)
	err = windows.CreateProcess(
		nil,
		cmdLine,
		nil,
		nil,
		false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		&envBlock[0],
		dirPtr,
		&si.startupInfo,
		pi,
	)
	runtime.KeepAlive(si)
	runtime.KeepAlive(attrList)
	runtime.KeepAlive(envBlock)
	if err != nil {
		return nil, err
	}
	windows.CloseHandle(pi.Thread)

	sess = &termSession{
		id:     newTermID(),
		dir:    dir,
		pc:     pc,
		in:     inputWrite,
		out:    outputRead,
		ptyIn:  ptyIn,
		ptyOut: ptyOut,
		proc:   pi.Process,
		subs:   map[chan []byte]struct{}{},
	}
	go sess.readLoop()
	return sess, nil
}

func (s *termSession) readLoop() {
	buf := make([]byte, 4096)
	for {
		var done uint32
		err := windows.ReadFile(s.out, buf, &done, nil)
		if done > 0 {
			s.publish(buf[:done])
		}
		if err != nil {
			s.mu.Lock()
			s.lastErr = err.Error()
			s.mu.Unlock()
			s.shutdown()
			return
		}
	}
}

func writeHandle(h windows.Handle, p []byte) error {
	for len(p) > 0 {
		var done uint32
		if err := windows.WriteFile(h, p, &done, nil); err != nil {
			return err
		}
		if done == 0 {
			return fmt.Errorf("terminal write wrote nothing")
		}
		p = p[done:]
	}
	return nil
}

func (s *termSession) publish(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return
	}
	s.history = append(s.history, p...)
	if len(s.history) > 512*1024 {
		s.history = append([]byte(nil), s.history[len(s.history)-512*1024:]...)
	}
	chunk := append([]byte(nil), p...)
	for ch := range s.subs {
		select {
		case ch <- chunk:
		default:
		}
	}
}

func (s *termSession) subscribe() (chan []byte, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan []byte, 256)
	if s.subs == nil {
		s.subs = map[chan []byte]struct{}{}
	}
	s.subs[ch] = struct{}{}
	return ch, append([]byte(nil), s.history...)
}

func (s *termSession) unsubscribe(ch chan []byte) {
	s.mu.Lock()
	delete(s.subs, ch)
	s.mu.Unlock()
}

func (s *termSession) shutdown() {
	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		return
	}
	s.dead = true
	subs := s.subs
	s.subs = nil
	s.mu.Unlock()
	for ch := range subs {
		close(ch)
	}
	if s.pc != 0 {
		windows.ClosePseudoConsole(s.pc)
	}
	if s.proc != 0 {
		windows.TerminateProcess(s.proc, 1)
		windows.CloseHandle(s.proc)
	}
	if s.in != 0 {
		windows.CloseHandle(s.in)
	}
	if s.out != 0 {
		windows.CloseHandle(s.out)
	}
	if s.ptyIn != 0 {
		windows.CloseHandle(s.ptyIn)
	}
	if s.ptyOut != 0 {
		windows.CloseHandle(s.ptyOut)
	}
	termMu.Lock()
	if terms[s.id] == s {
		delete(terms, s.id)
	}
	termMu.Unlock()
}

func writeTermChunk(w http.ResponseWriter, flusher http.Flusher, p []byte) {
	if len(p) == 0 {
		return
	}
	raw, _ := json.Marshal(map[string]string{"b64": base64.StdEncoding.EncodeToString(p)})
	fmt.Fprintf(w, "event: data\ndata: %s\n\n", raw)
	flusher.Flush()
}

func utf16EnvBlock(env []string) ([]uint16, error) {
	var buf []uint16
	for _, entry := range env {
		u, err := windows.UTF16FromString(entry)
		if err != nil {
			return nil, err
		}
		buf = append(buf, u...)
	}
	buf = append(buf, 0)
	return buf, nil
}

func newTermID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "term"
	}
	return hex.EncodeToString(b[:])
}
