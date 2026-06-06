package agentping

import (
	"io"
	"log"
	"os"
	"sync"
)

var (
	warnOnceMu  sync.Mutex
	warned      = map[string]struct{}{}
	warnLogger  = log.New(os.Stderr, "agentping: ", 0)
	warnLoggerW io.Writer
)

// warnOnce logs a message at most once per error class for the lifetime of
// the process. Output goes to stderr by default.
func warnOnce(class, msg string) {
	warnOnceMu.Lock()
	defer warnOnceMu.Unlock()
	if _, seen := warned[class]; seen {
		return
	}
	warned[class] = struct{}{}
	warnLogger.Println(msg)
}

// setWarnOutput redirects warning output. Intended for tests.
func setWarnOutput(w io.Writer) {
	warnOnceMu.Lock()
	defer warnOnceMu.Unlock()
	warnLoggerW = w
	if w == nil {
		warnLogger = log.New(os.Stderr, "agentping: ", 0)
	} else {
		warnLogger = log.New(w, "agentping: ", 0)
	}
}

// resetWarningsForTest clears the dedupe map. Tests only.
func resetWarningsForTest() {
	warnOnceMu.Lock()
	defer warnOnceMu.Unlock()
	warned = map[string]struct{}{}
}
