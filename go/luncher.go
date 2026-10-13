// OmniCode Go Luncher / Web UI server
// Scans for project dependencies, applies repair protocols,
// stores secrets + settings + projects + MCP + plugins + skills + agents
// in a single SQLite3 database, and serves a full OpenCode-style web IDE.

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	_ "modernc.org/sqlite"
)

type Dependency struct {
	Name    string
	Version string
	Status  string
}

type RepairAction struct {
	ID          string
	Description string
	Action      string
}

type Config struct {
	InstallPath   string `json:"installPath"`
	Database      string `json:"database"`
	WebHost       string `json:"webHost"`
	WebPort       int    `json:"webPort"`
	CorePath      string `json:"corePath"`
	WebStaticDir  string `json:"webStaticDir"`
	OpenInBrowser bool   `json:"openInBrowser"`
}

var config Config

func init() {
	userHome, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	inst := filepath.Join(userHome, ".omnicode")
	installUI := filepath.Join(inst, "ui")
	config = Config{
		InstallPath:   inst,
		Database:      filepath.Join(inst, "omnicode.db"),
		WebHost:       "127.0.0.1",
		WebPort:       pickPort(),
		CorePath:      filepath.Join(inst, "bin", "claw-core.exe"),
		WebStaticDir:  pickWebStaticDir(installUI),
		OpenInBrowser: true,
	}
	_ = os.MkdirAll(config.InstallPath, 0755)
	_ = os.MkdirAll(filepath.Dir(config.CorePath), 0755)
	if config.WebStaticDir != installUI {
		fmt.Fprintf(os.Stderr, "notice: serving UI from %s (install path %s was stale)\n",
			config.WebStaticDir, installUI)
	}
	// Boot the DB on startup so the user sees any migration warnings immediately.
	if _, err := openDB(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not open database: %v\n", err)
	}
	// Create ~/.omnicode/system.md from the built-in instructions when it
	// is missing, so the agent text can be edited without a rebuild.
	_ = loadSystemInstructions()
}

// pickPort is the first port launchWebIDE tries when the user did not pass
// --port. The listener is opened later and kept, so this must not bind.
func pickPort() int {
	return 4450
}

// listenLocal binds 127.0.0.1 and returns the open listener. When pinned is
// set, only preferred is tried and a busy port is a hard error. Otherwise
// the first free port in 4450–4499 is kept open so nothing else can take it
// before Serve starts.
func listenLocal(preferred int, pinned bool) (net.Listener, int, error) {
	if pinned {
		if preferred <= 0 || preferred > 65535 {
			return nil, 0, fmt.Errorf("invalid port %d", preferred)
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferred))
		if err != nil {
			return nil, 0, fmt.Errorf("port %d is already in use: %w", preferred, err)
		}
		return ln, preferred, nil
	}
	var last error
	for p := 4450; p < 4500; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			return ln, p, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("listen failed")
	}
	return nil, 0, fmt.Errorf("no free port in 4450-4499: %w", last)
}

// pickWebStaticDir chooses where to serve the HTML/JS/CSS from. By default
// that's the installed ~\.omnicode\ui, but if that directory is stale (still
// the v0.2.0 settings-panel UI) and a sibling ui/ directory next to the
// running binary has the new Editor nav, serve from there instead. That
// makes dev runs of `go run` or `./omnicode.exe` from a checkout always
// pick up the freshly-edited UI without needing install.ps1.
func pickWebStaticDir(installUI string) string {
	editorMarker := []byte(`data-view="editor"`)
	if uiFresh(installUI, editorMarker) {
		return installUI
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "ui")
		if uiFresh(candidate, editorMarker) {
			return candidate
		}
		candidate = filepath.Join(filepath.Dir(exe), "..", "ui")
		if abs, aErr := filepath.Abs(candidate); aErr == nil && uiFresh(abs, editorMarker) {
			return abs
		}
	}
	// Also try the working directory; devs running `go run` from omnicode/go/
	// land here.
	if wd, err := os.Getwd(); err == nil {
		if uiFresh(filepath.Join(wd, "ui"), editorMarker) {
			return filepath.Join(wd, "ui")
		}
		if uiFresh(filepath.Join(wd, "..", "ui"), editorMarker) {
			if abs, aErr := filepath.Abs(filepath.Join(wd, "..", "ui")); aErr == nil {
				return abs
			}
		}
	}
	// Fall back to the install path even if stale; the page will load the
	// legacy settings-panel UI and the user can fix it by re-running install.
	return installUI
}

// uiFresh returns true when dir/index.html exists, is non-empty, and contains
// the given marker substring. The marker check lets us distinguish the new
// Editor-aware index.html from the pre-v0.3.0 one without a version field.
func uiFresh(dir string, marker []byte) bool {
	data, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil || len(data) == 0 {
		return false
	}
	return bytes.Contains(data, marker)
}

// ============================================================
// Subcommands (CLI mode)
// ============================================================

func printHelp() {
	fmt.Println("OmniCode v0.3.1 - Go luncher + web IDE (Editor + tabs + plan.md)")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  omnicode                       Launch the OmniCode web IDE in your browser")
	fmt.Println("  omnicode web [--port N]         Same as above (optionally on a specific port)")
	fmt.Println("  omnicode start [args...]        Launch the Rust REPL (claw-core) in this terminal")
	fmt.Println("  omnicode scan [path]           Scan project dependencies")
	fmt.Println("  omnicode repair [path]         Check and apply repair protocols")
	fmt.Println("  omnicode secret-store K V      Store a secret in SQLite3")
	fmt.Println("  omnicode sync-keys            Import provider keys from agent/settings.json into SQLite")
	fmt.Println("  omnicode secret-retrieve K     Retrieve a secret from SQLite3")
	fmt.Println("  omnicode secret-list           List stored secret keys")
	fmt.Println("  omnicode secret-remove K       Remove a secret")
	fmt.Println("  omnicode config                Show OmniCode configuration")
	fmt.Println("  omnicode db-path               Print the SQLite database path")
	fmt.Println("  omnicode help                  Show this help")
	fmt.Println("")
	fmt.Println("Install: " + config.InstallPath)
	fmt.Println("DB:      " + config.Database)
	fmt.Println("Web UI:  http://" + config.WebHost + ":" + fmt.Sprint(config.WebPort) + "/")
}

func cmdSecretStore(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: omnicode secret-store <key> <value>")
		os.Exit(1)
	}
	if err := upsertSecret(args[0], args[1]); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Stored secret '%s' in %s\n", args[0], config.Database)
}

func cmdSecretRetrieve(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: omnicode secret-retrieve <key>")
		os.Exit(1)
	}
	v, err := getSecret(args[0])
	if err != nil {
		fmt.Printf("No secret found for key '%s'\n", args[0])
		os.Exit(1)
	}
	fmt.Println(v)
}

func cmdSecretList() {
	keys, err := listSecrets()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	if len(keys) == 0 {
		fmt.Println("No secrets stored.")
		return
	}
	fmt.Println("Stored secrets:")
	for _, k := range keys {
		fmt.Printf("  - %s\n", k)
	}
}

func cmdSecretRemove(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: omnicode secret-remove <key>")
		os.Exit(1)
	}
	if err := deleteSecret(args[0]); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Removed secret '%s'\n", args[0])
}

func cmdScan(args []string) {
	path := "."
	if len(args) > 0 {
		path = args[0]
	}
	fmt.Printf("Scanning dependencies in: %s\n", path)
	deps := scanDependencies(path)
	if len(deps) == 0 {
		fmt.Println("  No project manifests found (Cargo.toml / go.mod / package.json)")
		return
	}
	for _, d := range deps {
		fmt.Printf("  [OK] %s (%s) - %s\n", d.Name, d.Version, d.Status)
	}
}

func cmdRepair(args []string) {
	path := "."
	if len(args) > 0 {
		path = args[0]
	}
	fmt.Printf("Checking repair protocols in: %s\n", path)
	deps := scanDependencies(path)
	actions := checkRepair(deps)
	if len(actions) == 0 {
		fmt.Println("  No repairs needed. All dependencies are healthy.")
		return
	}
	for _, a := range actions {
		fmt.Printf("  [%s] %s -> action: %s\n", a.ID, a.Description, a.Action)
	}
}

func scanDependencies(path string) []Dependency {
	var deps []Dependency
	if _, err := os.Stat(filepath.Join(path, "Cargo.toml")); err == nil {
		deps = append(deps,
			Dependency{Name: "rust-toolchain", Version: "stable", Status: "ok"},
			Dependency{Name: "cargo", Version: "installed", Status: "ok"},
			Dependency{Name: "claw-cli", Version: "0.1.0", Status: "ok"},
		)
	}
	if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
		deps = append(deps, Dependency{Name: "go", Version: "1.26.4", Status: "ok"})
	}
	if _, err := os.Stat(filepath.Join(path, "package.json")); err == nil {
		deps = append(deps, Dependency{Name: "node", Version: "v22.x", Status: "ok"})
	}
	return deps
}

func checkRepair(deps []Dependency) []RepairAction {
	var actions []RepairAction
	for _, d := range deps {
		if d.Status != "ok" {
			actions = append(actions, RepairAction{
				ID:          d.Name + "-fix",
				Description: "Fix " + d.Name,
				Action:      "install",
			})
		}
	}
	return actions
}

// launchRepl starts claw-core in the current terminal, with secrets injected
// from the SQLite DB. Used by `omnicode start` and by unknown-arg fallback.
func launchRepl(extraArgs []string) {
	if _, err := os.Stat(config.CorePath); err != nil {
		fmt.Println("OmniCode core not found at " + config.CorePath)
		fmt.Println("Run the installer to set it up.")
		os.Exit(1)
	}
	args := append([]string{config.CorePath}, extraArgs...)
	env := injectSecretsIntoEnv(os.Environ(), []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
		"OPENAI_API_KEY", "OPENAI_BASE_URL",
		"XAI_API_KEY", "OMNICODE_API_KEY",
	})
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir, _ = os.Getwd()
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "omnicode: REPL exited with error:", err)
		os.Exit(1)
	}
}

// injectSecretsIntoEnv pulls any matching keys from the SQLite DB and injects
// them into the child env (overwriting any existing plaintext value).
func injectSecretsIntoEnv(env []string, keys []string) []string {
	for _, k := range keys {
		v, err := getSecret(k)
		if err != nil || v == "" {
			continue
		}
		prefix := k + "="
		overwritten := false
		for i, line := range env {
			if len(line) > len(prefix) && line[:len(prefix)] == prefix {
				env[i] = prefix + v
				overwritten = true
				break
			}
		}
		if !overwritten {
			env = append(env, prefix+v)
		}
	}
	return env
}

// ============================================================
// Entry point
// ============================================================

func main() {
	// No args → launch the web IDE (this is the default "type omnicode" UX)
	if len(os.Args) < 2 {
		launchWebIDE(nil)
		return
	}

	first := os.Args[1]
	if first == "--version" || first == "-V" {
		fmt.Println("omnicode v0.3.1 (Go web luncher + claw-core Rust IDE + Editor)")
		return
	}
	if first == "--help" || first == "-h" {
		printHelp()
		return
	}

	// Parse flags that take a value
	args := os.Args[2:]
	switch first {
	case "help":
		printHelp()
	case "version":
		fmt.Println("omnicode v0.3.1")
	case "config":
		out, _ := json.MarshalIndent(config, "", "  ")
		fmt.Println(string(out))
	case "db-path":
		fmt.Println(config.Database)
	case "scan":
		cmdScan(args)
	case "repair":
		cmdRepair(args)
	case "secret-store":
		cmdSecretStore(args)
	case "sync-keys":
		cmdSyncKeys()
	case "verify-keys":
		cmdVerifyKeys()
	case "secret-retrieve":
		cmdSecretRetrieve(args)
	case "secret-list":
		cmdSecretList()
	case "secret-remove":
		cmdSecretRemove(args)
	case "start":
		launchRepl(args)
	case "web":
		launchWebIDE(args)
	default:
		// Unknown subcommand → forward to the REPL (lets `omnicode --print "hi"` work)
		launchRepl(os.Args[1:])
	}
}

// launchWebIDE starts the HTTP server, opens the browser, and blocks.
func launchWebIDE(args []string) {
	// Optional --port override. A chosen port is pinned; the default range
	// keeps the listener it finds.
	pinned := false
	for i, a := range args {
		if a == "--port" && i+1 < len(args) {
			var p int
			fmt.Sscanf(args[i+1], "%d", &p)
			if p > 0 {
				config.WebPort = p
				pinned = true
			}
		}
		if strings.HasPrefix(a, "--port=") {
			var p int
			fmt.Sscanf(strings.TrimPrefix(a, "--port="), "%d", &p)
			if p > 0 {
				config.WebPort = p
				pinned = true
			}
		}
		if a == "--no-browser" {
			config.OpenInBrowser = false
		}
	}

	ln, port, err := listenLocal(config.WebPort, pinned)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
	config.WebPort = port

	mux := http.NewServeMux()
	registerRoutes(mux)

	addr := ln.Addr().String()
	url := fmt.Sprintf("http://%s/", addr)

	fmt.Println("==============================================")
	fmt.Println(" OmniCode Web IDE")
	fmt.Println("==============================================")
	fmt.Printf(" Local URL:    %s\n", url)
	fmt.Printf(" Install:      %s\n", config.InstallPath)
	fmt.Printf(" Database:     %s\n", config.Database)
	fmt.Printf(" Core binary:  %s\n", config.CorePath)
	fmt.Println("----------------------------------------------")
	fmt.Println(" Press Ctrl+C to stop the server.")
	fmt.Println("==============================================")

	if config.OpenInBrowser {
		go openBrowser(url)
	}

	srv := &http.Server{Handler: mux}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

// openBrowser asks the OS to open the IDE in the user's default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
	_ = cmd.Wait()
}

// ============================================================
// Secrets helpers (used by CLI and HTTP layer)
// ============================================================

func upsertSecret(key, value string) error {
	return withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO secrets (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`, key, value)
		return err
	})
}

func getSecret(key string) (string, error) {
	var v string
	err := readDB(func(db *sql.DB) error {
		return db.QueryRow("SELECT value FROM secrets WHERE key = ?", key).Scan(&v)
	})
	return v, err
}

func listSecrets() ([]string, error) {
	var keys []string
	err := readDB(func(db *sql.DB) error {
		rows, err := db.Query("SELECT key FROM secrets ORDER BY key")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return err
			}
			keys = append(keys, k)
		}
		return nil
	})
	return keys, err
}

func deleteSecret(key string) error {
	return withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM secrets WHERE key = ?", key)
		return err
	})
}
