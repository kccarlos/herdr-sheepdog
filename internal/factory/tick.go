package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

const (
	// TickTerminalName is the single herdr-core terminal name under which the
	// daemon keeps the factory tick loop alive (`hc run --keep`). Distinct from
	// Shepherdr's `herdr-automations` so the two can coexist during Phase 2–4.
	TickTerminalName = "sheepdog-factory-tick"

	// LabelRole, LabelEngineDigest, LabelPython, and LabelStore tag the tick
	// terminal in herdr-core so the daemon can adopt a matching live terminal
	// across daemon restarts and replace stale builds after an update.
	LabelRole         = "sheepdog.role"
	LabelEngineDigest = "sheepdog.engine"
	LabelPython       = "sheepdog.python"
	LabelStore        = "sheepdog.store"

	// RoleTick is the sheepdog.role label value of the factory tick terminal.
	RoleTick = "tick"

	defaultTickPoll        = 10 * time.Second
	defaultTickMinBackoff  = time.Second
	defaultTickMaxBackoff  = 30 * time.Second
	defaultTickHealthyWait = 30 * time.Second
	defaultTickHCTimeout   = 5 * time.Second

	// tickBootstrapScript prepends the managed CPython bin directory and the
	// unpacked engine's CLI directories onto herdr-core's own user-session PATH
	// without overwriting herdr-core's PATH with a minimal launchd daemon PATH,
	// then execs the managed Python interpreter directly in place.
	tickBootstrapScript = `export PATH="$1${PATH:+:$PATH}" PYTHONDONTWRITEBYTECODE=1 PYTHONNOUSERSITE=1; shift; exec "$@"`
)

var shellNames = map[string]bool{
	"bash": true, "zsh": true, "fish": true, "sh": true, "dash": true,
	"ksh": true, "tcsh": true, "csh": true, "nu": true, "xonsh": true,
	"pwsh": true, "powershell": true, "elvish": true,
}

// TickOptions configures TickSupervisor. Zero values take the machine defaults.
type TickOptions struct {
	DataDir        string
	TasksDir       string
	SettingsPath   string
	HCSocket       string
	Home           string
	TempDir        string
	Getenv         func(string) string
	Engine         EngineSource
	NoPythonRepair bool
	LookPath       func(string) (string, error)
	RunCommand     CommandFunc
	Log            logging.Logger

	PollInterval time.Duration
	MinBackoff   time.Duration
	MaxBackoff   time.Duration
	HealthyAfter time.Duration
	HCTimeout    time.Duration
}

func (o TickOptions) withDefaults() (TickOptions, bool, bool) {
	getenv := o.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	o.Getenv = getenv
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	if o.TempDir == "" {
		o.TempDir = os.TempDir()
	}
	defDataDir, _ := defaultFactoryLocations(o.Home, getenv)
	if o.DataDir == "" {
		o.DataDir = defDataDir
	}
	tasksDir, settingsPath, customStore, customSettings := ResolveFactoryPaths(o.DataDir, o.Home, getenv)
	if strings.TrimSpace(o.TasksDir) != "" {
		tasksDir = filepath.Clean(o.TasksDir)
		customStore = !sameCleanPath(tasksDir, filepath.Join(defDataDir, "tasks"))
	}
	if strings.TrimSpace(o.SettingsPath) != "" {
		settingsPath = filepath.Clean(o.SettingsPath)
		_, defSettings := defaultFactoryLocations(o.Home, getenv)
		customSettings = !sameCleanPath(settingsPath, defSettings)
	}
	o.TasksDir = tasksDir
	o.SettingsPath = settingsPath
	if o.HCSocket == "" {
		o.HCSocket = hc.DefaultSocket(getenv, o.Home, os.Getuid(), nil)
	}
	if o.PollInterval <= 0 {
		o.PollInterval = defaultTickPoll
	}
	if o.MinBackoff <= 0 {
		o.MinBackoff = defaultTickMinBackoff
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = defaultTickMaxBackoff
	}
	if o.HealthyAfter <= 0 {
		o.HealthyAfter = defaultTickHealthyWait
	}
	if o.HCTimeout <= 0 {
		o.HCTimeout = defaultTickHCTimeout
	}
	return o, customStore, customSettings
}

func defaultFactoryLocations(home string, getenv func(string) string) (dataDir, settingsPath string) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	for _, k := range []string{"SHEEPDOG_FACTORY_HOME", "SHEEPDOG_HOME", "SHEPHERDR_HOME"} {
		if v := strings.TrimSpace(getenv(k)); filepath.IsAbs(v) {
			v = filepath.Clean(v)
			return filepath.Join(v, "data"), filepath.Join(v, "config", "settings.json")
		}
	}
	dataRoot := filepath.Join(home, ".local", "share")
	if v := strings.TrimSpace(getenv("XDG_DATA_HOME")); filepath.IsAbs(v) {
		dataRoot = filepath.Clean(v)
	}
	cfgRoot := filepath.Join(home, ".config")
	if v := strings.TrimSpace(getenv("XDG_CONFIG_HOME")); filepath.IsAbs(v) {
		cfgRoot = filepath.Clean(v)
	}
	return filepath.Join(dataRoot, "sheepdog"), filepath.Join(cfgRoot, "sheepdog", "settings.json")
}

// ResolveFactoryPaths resolves the task-store directory and settings.json path
// for dataDir. When dataDir matches the standard XDG / SHEEPDOG_HOME location,
// settingsPath uses the matching config root (~/.config/sheepdog/settings.json).
// When dataDir is overridden (such as `sheepdog serve --data-dir` or a test's
// temporary directory), settingsPath stays inside dataDir unless explicitly set
// via HERDR_TASK_SETTINGS, so scratch daemons never read ~/.config/sheepdog.
func ResolveFactoryPaths(dataDir, home string, getenv func(string) string) (tasksDir, settingsPath string, customStore, customSettings bool) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	defDataDir, defSettings := defaultFactoryLocations(home, getenv)
	if strings.TrimSpace(dataDir) == "" {
		dataDir = defDataDir
	}
	dataDir = filepath.Clean(dataDir)
	isCustomData := !sameCleanPath(dataDir, defDataDir)

	if s := strings.TrimSpace(getenv("HERDR_TASK_STORE")); s != "" {
		tasksDir = filepath.Clean(s)
		customStore = !sameCleanPath(tasksDir, filepath.Join(defDataDir, "tasks"))
	} else {
		tasksDir = filepath.Join(dataDir, "tasks")
		customStore = isCustomData
	}

	if p := strings.TrimSpace(getenv("HERDR_TASK_SETTINGS")); p != "" {
		settingsPath = filepath.Clean(p)
		customSettings = !sameCleanPath(settingsPath, defSettings)
	} else if !isCustomData {
		settingsPath = defSettings
		customSettings = false
	} else {
		settingsPath = filepath.Join(dataDir, "settings.json")
		customSettings = true
	}
	return tasksDir, settingsPath, customStore, customSettings
}

func sameCleanPath(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// HasStartedProject reports whether at least one factory project has been
// started in settingsPath or tasksDir. It reads only JSON metadata in those
// locations and never touches a project's git repository.
func HasStartedProject(tasksDir, settingsPath string) bool {
	return hasSettingsProject(settingsPath) || hasTaskStoreProject(tasksDir) || hasAutopilotProject(tasksDir)
}

func hasSettingsProject(settingsPath string) bool {
	if strings.TrimSpace(settingsPath) == "" {
		return false
	}
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return false
	}
	var doc struct {
		Projects map[string]json.RawMessage `json:"projects"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	for k, v := range doc.Projects {
		if strings.TrimSpace(k) != "" && bytes.HasPrefix(bytes.TrimSpace(v), []byte("{")) {
			return true
		}
	}
	return false
}

func hasTaskStoreProject(tasksDir string) bool {
	if strings.TrimSpace(tasksDir) == "" {
		return false
	}
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".archive.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(tasksDir, e.Name()))
		if err != nil {
			continue
		}
		if isTaskListJSON(raw) {
			return true
		}
	}
	return false
}

func isTaskListJSON(raw []byte) bool {
	var doc struct {
		Folder *string          `json:"folder"`
		Tasks  *json.RawMessage `json:"tasks"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Folder == nil || doc.Tasks == nil {
		return false
	}
	return strings.TrimSpace(*doc.Folder) != "" && bytes.HasPrefix(bytes.TrimSpace(*doc.Tasks), []byte("["))
}

func hasAutopilotProject(tasksDir string) bool {
	if strings.TrimSpace(tasksDir) == "" {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(tasksDir, ".autopilot.json"))
	if err != nil {
		return false
	}
	var doc struct {
		Projects map[string]struct {
			On *bool `json:"on"`
		} `json:"projects"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	for k, v := range doc.Projects {
		if strings.TrimSpace(k) != "" && (v.On == nil || *v.On) {
			return true
		}
	}
	return false
}

// TickSupervisor keeps one factory tick loop alive as a herdr-core terminal
// (`hc run --keep`) once at least one factory project is started. Until a
// project is started it stays idle: it unpacks nothing, installs nothing, and
// opens no terminal in herdr-core.
type TickSupervisor struct {
	opts           TickOptions
	customStore    bool
	customSettings bool
	client         *hc.Client
	wake           chan struct{}

	mu          sync.Mutex
	managedAddr string
	prepared    bool
	eng         Engine
	py          ManagedPython
	startedAt   time.Time
	backoff     time.Duration

	lastErrStage string
	lastErrMsg   string

	sub *hc.Subscription
}

// NewTickSupervisor creates a TickSupervisor for opts.
func NewTickSupervisor(opts TickOptions) *TickSupervisor {
	resolved, customStore, customSettings := opts.withDefaults()
	return &TickSupervisor{
		opts:           resolved,
		customStore:    customStore,
		customSettings: customSettings,
		client:         &hc.Client{Socket: resolved.HCSocket, Timeout: resolved.HCTimeout},
		wake:           make(chan struct{}, 1),
	}
}

// Wake nudges Run to check project state and the tick terminal immediately.
// Safe to call from any goroutine.
func (s *TickSupervisor) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// TerminalAddress returns the herdr-core address of the currently adopted or
// started tick terminal, or "" when inactive.
func (s *TickSupervisor) TerminalAddress() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.managedAddr
}

// Run supervises the factory tick terminal until ctx is cancelled. Because the
// tick terminal is started with `Keep: true` (`hc run --keep`), cancelling ctx
// closes only the daemon's watch subscription and leaves a healthy tick loop
// running across daemon restarts.
func (s *TickSupervisor) Run(ctx context.Context) {
	defer s.closeSub()

	ticker := time.NewTicker(s.opts.PollInterval)
	defer ticker.Stop()

	var retryTimer *time.Timer
	var retryCh <-chan time.Time
	stopRetry := func() {
		if retryTimer != nil {
			retryTimer.Stop()
			retryTimer = nil
			retryCh = nil
		}
	}
	defer stopRetry()

	for {
		wait := s.reconcile(ctx)
		stopRetry()
		if wait > 0 {
			retryTimer = time.NewTimer(wait)
			retryCh = retryTimer.C
		}

		var evCh <-chan hc.Notification
		if s.sub != nil {
			evCh = s.sub.Events()
		}

		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-retryCh:
			stopRetry()
		case <-ticker.C:
		case n, ok := <-evCh:
			if !ok {
				s.closeSub()
				continue
			}
			if !shouldReconcileOnEvent(n) {
				continue
			}
		}
	}
}

func shouldReconcileOnEvent(n hc.Notification) bool {
	switch n.Method {
	case "missed":
		return true
	case "event":
		switch n.Type {
		case "finished", "lost", "removed", "restarted", "foreground", "labels":
			return true
		}
	}
	return false
}

func (s *TickSupervisor) reconcile(ctx context.Context) time.Duration {
	if ctx.Err() != nil {
		return 0
	}
	if !HasStartedProject(s.opts.TasksDir, s.opts.SettingsPath) {
		s.stopWhenNoProjects(ctx)
		return 0
	}

	if !s.prepared {
		eng, py, stage, err := s.prepareRuntime(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.logError(stage, err)
			}
			return s.advanceBackoff()
		}
		s.eng = eng
		s.py = py
		s.prepared = true
	}

	existing, getErr := s.client.Get(ctx, TickTerminalName)
	haveExisting := getErr == nil
	if getErr != nil && !errors.Is(getErr, hc.ErrNotFound) {
		s.closeSub()
		if ctx.Err() == nil {
			s.logError("hc_get", getErr)
		}
		return 0
	}

	storeTag := s.storeLabelValue()
	if !s.startedAt.IsZero() && time.Since(s.startedAt) >= s.opts.HealthyAfter {
		s.startedAt = time.Time{}
		s.backoff = 0
	}
	if haveExisting && tickTerminalMatches(existing, s.eng, s.py, storeTag) {
		s.clearError()
		s.mu.Lock()
		firstAdopt := s.managedAddr != existing.Address
		s.managedAddr = existing.Address
		s.mu.Unlock()
		if firstAdopt {
			s.logEvent("factory.tick_adopted", existing.Address, map[string]any{
				"version": s.eng.VersionKey,
				"python":  s.py.Version,
			})
		}
		s.ensureSub(ctx)
		return 0
	}

	// Respect restart backoff if the previous start exited quickly.
	if !s.startedAt.IsZero() && s.backoff > 0 {
		if rem := s.backoff - time.Since(s.startedAt); rem > 0 {
			return rem
		}
	}

	// Re-verify (and repair if needed) the unpacked engine and managed Python
	// before spawning or replacing the tick terminal.
	eng, py, stage, err := s.prepareRuntime(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logError(stage, err)
		}
		return s.advanceBackoff()
	}
	s.eng = eng
	s.py = py
	s.prepared = true

	var (
		replacedAddr  string
		replaceReason string
	)
	if haveExisting {
		replacedAddr = existing.Address
		replaceReason = mismatchReason(existing, eng, py, storeTag)
		if err := s.removeStaleTerminal(ctx, existing); err != nil {
			if ctx.Err() == nil {
				s.logError("hc_cleanup", err)
			}
			return s.advanceBackoff()
		}
	}

	created, err := s.client.Run(ctx, hc.RunParams{
		Argv:     tickArgv(py, eng),
		Cwd:      s.tickCwd(),
		Name:     TickTerminalName,
		Env:      s.tickEnv(),
		Labels:   s.tickLabels(eng, py, storeTag),
		Keep:     true,
		NoParent: true,
		NoNotify: true,
	})
	if err != nil {
		if errors.Is(err, hc.ErrNameTaken) {
			// Another process created the terminal in the meantime; inspect it
			// on the next pass without penalising backoff.
			s.Wake()
			return 0
		}
		if ctx.Err() == nil {
			s.logError("hc_run", err)
		}
		return s.advanceBackoff()
	}

	s.clearError()
	s.mu.Lock()
	s.managedAddr = created.Address
	s.mu.Unlock()
	s.startedAt = time.Now()
	_ = s.advanceBackoff()
	s.ensureSub(ctx)

	payload := map[string]any{
		"version": eng.VersionKey,
		"python":  py.Version,
	}
	if replacedAddr != "" {
		payload["replaced"] = replacedAddr
		payload["reason"] = replaceReason
	}
	s.logEvent("factory.tick_started", created.Address, payload)
	return 0
}

func (s *TickSupervisor) stopWhenNoProjects(ctx context.Context) {
	s.closeSub()
	s.clearError()
	s.startedAt = time.Time{}
	s.backoff = 0

	s.mu.Lock()
	prev := s.managedAddr
	s.mu.Unlock()
	if prev == "" {
		return
	}

	t, err := s.client.Get(ctx, TickTerminalName)
	if err != nil {
		if errors.Is(err, hc.ErrNotFound) {
			s.mu.Lock()
			s.managedAddr = ""
			s.mu.Unlock()
		}
		return
	}
	if t.Labels[LabelRole] != RoleTick || t.Labels[LabelStore] != s.storeLabelValue() {
		s.mu.Lock()
		s.managedAddr = ""
		s.mu.Unlock()
		return
	}
	if err := s.removeStaleTerminal(ctx, t); err != nil {
		return
	}
	s.mu.Lock()
	s.managedAddr = ""
	s.mu.Unlock()
	s.logEvent("factory.tick_stopped", t.Address, map[string]any{"reason": "no_projects"})
}

func (s *TickSupervisor) prepareRuntime(ctx context.Context) (Engine, ManagedPython, string, error) {
	var (
		eng Engine
		err error
	)
	if s.opts.NoPythonRepair {
		eng, err = InspectEngine(s.opts.DataDir, s.opts.Engine)
	} else {
		eng, err = EnsureEngine(s.opts.DataDir, s.opts.Engine)
	}
	if err != nil {
		return Engine{}, ManagedPython{}, "engine", err
	}
	lp := s.opts.LookPath
	if lp == nil {
		lp = exec.LookPath
	}
	var customLP func(string) (string, error)
	if s.opts.LookPath != nil {
		customLP = s.opts.LookPath
	}
	py, err := EnsurePython(ctx, PythonOptions{
		DataDir:       s.opts.DataDir,
		PinnedVersion: eng.PythonVersion,
		Home:          s.opts.Home,
		TempDir:       s.opts.TempDir,
		NoRepair:      s.opts.NoPythonRepair,
		LookPath:      customLP,
		RunCommand:    s.opts.RunCommand,
	})
	if err != nil {
		return Engine{}, ManagedPython{}, "python", err
	}
	_ = lp
	return eng, py, "", nil
}

func (s *TickSupervisor) removeStaleTerminal(ctx context.Context, t hc.Terminal) error {
	if t.State == "live" {
		if _, err := s.client.Kill(ctx, t.Address); err != nil && !errors.Is(err, hc.ErrNotFound) && !errors.Is(err, hc.ErrFinished) {
			return err
		}
	}
	for range 20 {
		if _, err := s.client.Remove(ctx, t.Address); err == nil || errors.Is(err, hc.ErrNotFound) {
			return nil
		} else if !errors.Is(err, hc.ErrLive) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	_, err := s.client.Remove(ctx, t.Address)
	if errors.Is(err, hc.ErrNotFound) {
		return nil
	}
	return err
}

func (s *TickSupervisor) ensureSub(ctx context.Context) {
	if s.sub != nil {
		return
	}
	sub, err := s.client.Subscribe(ctx, hc.SubscribeFilter{
		Labels: map[string]string{LabelRole: RoleTick},
	})
	if err != nil {
		return
	}
	s.sub = sub
}

func (s *TickSupervisor) closeSub() {
	if s.sub != nil {
		_ = s.sub.Close()
		s.sub = nil
	}
}

func (s *TickSupervisor) advanceBackoff() time.Duration {
	if s.backoff <= 0 {
		s.backoff = s.opts.MinBackoff
	} else {
		s.backoff = min(s.backoff*2, s.opts.MaxBackoff)
	}
	if s.startedAt.IsZero() {
		s.startedAt = time.Now()
	}
	return s.backoff
}

func (s *TickSupervisor) storeLabelValue() string {
	if s.customStore {
		return s.opts.TasksDir
	}
	return ""
}

func (s *TickSupervisor) tickLabels(eng Engine, py ManagedPython, storeTag string) map[string]string {
	labels := map[string]string{
		LabelRole:         RoleTick,
		LabelEngineDigest: eng.Digest,
		LabelPython:       py.Executable,
	}
	if storeTag != "" {
		labels[LabelStore] = storeTag
	}
	return labels
}

func (s *TickSupervisor) tickEnv() map[string]string {
	var env map[string]string
	set := func(k, v string) {
		if v == "" {
			return
		}
		if env == nil {
			env = map[string]string{}
		}
		env[k] = v
	}
	if s.customStore {
		set("HERDR_TASK_STORE", s.opts.TasksDir)
		if t := strings.TrimSpace(s.opts.Getenv("HERDR_TASK_TEMPLATES")); t != "" {
			set("HERDR_TASK_TEMPLATES", t)
		} else {
			set("HERDR_TASK_TEMPLATES", filepath.Join(s.opts.DataDir, "templates"))
		}
	} else if t := strings.TrimSpace(s.opts.Getenv("HERDR_TASK_TEMPLATES")); t != "" {
		set("HERDR_TASK_TEMPLATES", t)
	}
	if s.customSettings {
		set("HERDR_TASK_SETTINGS", s.opts.SettingsPath)
	}
	return env
}

func (s *TickSupervisor) tickCwd() string {
	if s.opts.Home != "" {
		if st, err := os.Stat(s.opts.Home); err == nil && st.IsDir() {
			return s.opts.Home
		}
	}
	return existingDir(s.opts.TempDir, ".")
}

func tickArgv(py ManagedPython, eng Engine) []string {
	var dirs []string
	for _, d := range []string{
		py.BinDir,
		filepath.Dir(eng.HerdrTask),
		filepath.Dir(eng.HerdrSecret),
	} {
		if d = strings.TrimSpace(d); d != "" && d != "." {
			dirs = append(dirs, d)
		}
	}
	return []string{
		"sh", "-c", tickBootstrapScript,
		TickTerminalName,
		strings.Join(dirs, string(os.PathListSeparator)),
		py.Executable,
		eng.HerdrTask,
		"scheduler",
	}
}

func tickTerminalMatches(t hc.Terminal, eng Engine, py ManagedPython, storeTag string) bool {
	return mismatchReason(t, eng, py, storeTag) == ""
}

func mismatchReason(t hc.Terminal, eng Engine, py ManagedPython, storeTag string) string {
	if t.State != "live" {
		return "finished"
	}
	if t.Labels[LabelRole] != RoleTick {
		return "unmanaged"
	}
	if t.Labels[LabelEngineDigest] != eng.Digest ||
		t.Labels[LabelPython] != py.Executable ||
		t.Labels[LabelStore] != storeTag {
		return "version_changed"
	}
	if !tickProgramRunning(t, eng) {
		return "shell"
	}
	return ""
}

func tickProgramRunning(t hc.Terminal, eng Engine) bool {
	if fg := t.Foreground; fg != nil {
		if len(fg.Argv) > 0 {
			return slices.Contains(fg.Argv, eng.HerdrTask) && slices.Contains(fg.Argv, "scheduler")
		}
		if fg.Name != "" && isShellBase(fg.Name) {
			return false
		}
	}
	return slices.Contains(t.Argv, eng.HerdrTask) && slices.Contains(t.Argv, "scheduler")
}

func isShellBase(name string) bool {
	base := strings.TrimPrefix(filepath.Base(name), "-")
	return shellNames[base]
}

func (s *TickSupervisor) logEvent(typ, paneID string, payload map[string]any) {
	if s.opts.Log != nil {
		s.opts.Log.Log("factory", typ, paneID, payload)
	}
}

func (s *TickSupervisor) logError(stage string, err error) {
	msg := cleanReason(err.Error())
	if s.lastErrStage == stage && s.lastErrMsg == msg {
		return
	}
	s.lastErrStage = stage
	s.lastErrMsg = msg
	s.logEvent("factory.tick_error", "", map[string]any{
		"stage": stage,
		"error": msg,
	})
}

func (s *TickSupervisor) clearError() {
	s.lastErrStage = ""
	s.lastErrMsg = ""
}
