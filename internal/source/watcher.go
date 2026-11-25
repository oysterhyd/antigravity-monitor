package source

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	defaultDebounce   = 150 * time.Millisecond
	summariesTimerKey = "__summaries__"
)

// watchEvent is emitted after per-conversation debouncing completes.
type watchEvent struct {
	Summaries bool
	ConvID    string
}

// dirWatcher wraps fsnotify to watch conversation_summaries.db(-wal), conversations/*.db(-wal),
// and active transcript log directories with independent per-conv_id 150ms debouncing.
type dirWatcher struct {
	rootDir  string
	convDir  string
	debounce time.Duration
	onEvent  func(watchEvent)

	fsw    *fsnotify.Watcher
	stopCh chan struct{}
	doneCh chan struct{}

	dirsMu      sync.Mutex
	watchedDirs map[string]bool

	mu     sync.Mutex
	closed bool
	timers map[string]*time.Timer
}

func newDirWatcher(rootDir string, debounce time.Duration, onEvent func(watchEvent)) (*dirWatcher, error) {
	if debounce <= 0 {
		debounce = defaultDebounce
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &dirWatcher{
		rootDir:     rootDir,
		convDir:     filepath.Join(rootDir, "conversations"),
		debounce:    debounce,
		onEvent:     onEvent,
		fsw:         fsw,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
		watchedDirs: make(map[string]bool),
		timers:      make(map[string]*time.Timer),
	}

	// Best-effort add rootDir and conversations dir if they exist.
	w.addDirIfExists(w.rootDir)
	w.addDirIfExists(w.convDir)

	go w.loop()
	return w, nil
}

func (w *dirWatcher) addDirIfExists(dir string) {
	clean := filepath.Clean(dir)
	if w.watchedDirs[clean] {
		return
	}
	if fi, err := os.Stat(clean); err != nil || !fi.IsDir() {
		return
	}
	if err := w.fsw.Add(clean); err == nil {
		w.watchedDirs[clean] = true
	}
}

// WatchTranscriptDirs ensures transcript log directories for the given conversation IDs
// are watched for real-time USER_INPUT / subagent Role updates.
func (w *dirWatcher) WatchTranscriptDirs(convIDs map[string]bool) {
	w.dirsMu.Lock()
	defer w.dirsMu.Unlock()

	dirs := make(map[string]bool, len(convIDs)+2)
	dirs[filepath.Clean(w.rootDir)] = true
	dirs[filepath.Clean(w.convDir)] = true
	for id := range convIDs {
		if id == "" {
			continue
		}
		dir := filepath.Dir(TranscriptPath(w.rootDir, id))
		dirs[dir] = true
	}
	for dir := range w.watchedDirs {
		if !dirs[dir] {
			_ = w.fsw.Remove(dir)
			delete(w.watchedDirs, dir)
		}
	}
	for dir := range dirs {
		w.addDirIfExists(dir)
	}
}

func (w *dirWatcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	for k, t := range w.timers {
		t.Stop()
		delete(w.timers, k)
	}
	close(w.stopCh)
	w.mu.Unlock()

	err := w.fsw.Close()
	<-w.doneCh
	return err
}

func (w *dirWatcher) loop() {
	defer close(w.doneCh)
	for {
		select {
		case <-w.stopCh:
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handleFSNotifyEvent(ev)
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			// Ignore transient OS watch errors; polling fallback still keeps state fresh.
		}
	}
}

func (w *dirWatcher) handleFSNotifyEvent(ev fsnotify.Event) {
	// Ignore pure chmod/remove noise unless write/create/rename occurred.
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
		return
	}
	name := filepath.Clean(ev.Name)
	base := strings.ToLower(filepath.Base(name))
	dir := filepath.Clean(filepath.Dir(name))

	// L1: conversation_summaries.db / -wal / -shm
	if strings.EqualFold(dir, filepath.Clean(w.rootDir)) {
		if base == "conversation_summaries.db" ||
			base == "conversation_summaries.db-wal" ||
			base == "conversation_summaries.db-shm" {
			w.schedule(summariesTimerKey, watchEvent{Summaries: true})
		}
		return
	}

	// L2: conversations/<conv_id>.db or <conv_id>.db-wal
	if strings.EqualFold(dir, filepath.Clean(w.convDir)) {
		if convID, ok := extractConvIDFromDBFile(filepath.Base(name)); ok {
			w.schedule(convID, watchEvent{ConvID: convID})
		}
		return
	}

	// L3: brain/<conv_id>/.system_generated/logs/transcript.jsonl
	if base == "transcript.jsonl" {
		if convID, ok := extractConvIDFromTranscriptPath(w.rootDir, name); ok {
			w.schedule(convID, watchEvent{ConvID: convID})
		}
	}
}

func (w *dirWatcher) schedule(key string, ev watchEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if t, ok := w.timers[key]; ok {
		t.Reset(w.debounce)
		return
	}
	w.timers[key] = time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		closed := w.closed
		delete(w.timers, key)
		w.mu.Unlock()
		if closed {
			return
		}
		if w.onEvent != nil {
			w.onEvent(ev)
		}
	})
}

func extractConvIDFromDBFile(filename string) (string, bool) {
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".db-wal"):
		id := filename[:len(filename)-len(".db-wal")]
		return id, id != ""
	case strings.HasSuffix(lower, ".db"):
		id := filename[:len(filename)-len(".db")]
		return id, id != ""
	default:
		return "", false
	}
}

func extractConvIDFromTranscriptPath(rootDir, fullPath string) (string, bool) {
	brainDir := filepath.Clean(filepath.Join(rootDir, "brain"))
	rel, err := filepath.Rel(brainDir, filepath.Clean(fullPath))
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 || parts[0] == "" || parts[0] == "." {
		return "", false
	}
	return parts[0], true
}
