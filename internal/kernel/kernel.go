// Package kernel manages long-lived interpreter subprocesses (Python, Node.js)
// so that REPL state (variables, imports, etc.) persists across successive
// Execute calls within the same session.
//
// Each interpreter is started with a custom bootstrap loop that:
//  1. Reads code from stdin until it sees a per-session sentinel line
//  2. Executes the code in a shared namespace
//  3. Prints the sentinel to stdout
//
// This avoids the noise (banners, prompts, echoed input) produced by the
// interpreters' built-in REPL modes.
package kernel

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Kernel is a single long-lived interpreter process.
// Goroutine-safe: concurrent Execute calls are serialized via mu.
type Kernel struct {
	lang   string
	cmd    string
	args   []string
	proc   *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
	dirty  bool   // true if last Execute timed out; state may be corrupt
	nonce  string // random hex suffix to form the per-kernel sentinel
}

// New spawns a Kernel for lang ("python" or "node").
// Returns an error if the interpreter is not found on PATH.
func New(lang string) (*Kernel, error) {
	nonce, err := randomNonce()
	if err != nil {
		return nil, fmt.Errorf("kernel: generate nonce: %w", err)
	}
	cmd, args, err := bootstrapForLang(lang, nonce)
	if err != nil {
		return nil, err
	}
	k := &Kernel{
		lang:  lang,
		cmd:   cmd,
		args:  args,
		nonce: nonce,
	}
	if err := k.start(); err != nil {
		return nil, err
	}
	return k, nil
}

// Execute sends code to the interpreter and returns its output.
// A default timeout of 30 s applies; callers may pass a shorter-deadline context.
// On timeout SIGINT is sent to the process group (not SIGKILL) so the
// interpreter survives; k.dirty is set to true.
// If the process has died it is respawned; the error from the crash is returned
// so the model can see that state was lost.
func (k *Kernel) Execute(ctx context.Context, code string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	// Apply default timeout if the context has no deadline.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	// If the kernel is dirty from a previous timeout, attempt recovery.
	if k.dirty {
		if err := k.recoverDirty(); err != nil {
			// Process died during recovery — respawn and return crash error.
			_ = k.respawn()
			return "", fmt.Errorf("kernel: process crashed during dirty recovery: %w", err)
		}
		k.dirty = false
	}

	sentinel := k.sentinel()

	// Write the code block followed by the sentinel line so the bootstrap knows
	// where the code ends.
	var payloadBuf strings.Builder
	payloadBuf.WriteString(code)
	payloadBuf.WriteString("\n")
	payloadBuf.WriteString(sentinel)
	payloadBuf.WriteString("\n")

	if _, err := io.WriteString(k.stdin, payloadBuf.String()); err != nil {
		// stdin closed → process likely died; respawn and surface error.
		_ = k.respawn()
		return "", fmt.Errorf("kernel: write to stdin: %w", err)
	}

	type readResult struct {
		out string
		err error
	}
	doneCh := make(chan readResult, 1)
	go func() {
		var out strings.Builder
		for {
			line, err := k.stdout.ReadString('\n')
			trimmed := strings.TrimRight(line, "\r\n")
			if trimmed == sentinel {
				doneCh <- readResult{strings.TrimRight(out.String(), "\n"), nil}
				return
			}
			if len(line) > 0 {
				out.WriteString(line)
			}
			if err != nil {
				doneCh <- readResult{strings.TrimRight(out.String(), "\n"), fmt.Errorf("kernel: read stdout: %w", err)}
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		sendInterrupt(k.proc.Process)
		k.dirty = true
		// Drain partial output with a short deadline.
		partial := ""
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer drainCancel()
		select {
		case res := <-doneCh:
			partial = res.out
		case <-drainCtx.Done():
		}
		return partial, fmt.Errorf("kernel: execute timed out: %w", ctx.Err())

	case res := <-doneCh:
		if res.err != nil {
			// EOF or read error → process died.
			_ = k.respawn()
			return res.out, fmt.Errorf("kernel: process died: %w", res.err)
		}
		k.dirty = false
		return res.out, nil
	}
}

// Close terminates the underlying process.
func (k *Kernel) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.kill()
	return nil
}

// --- internal helpers ---

func (k *Kernel) start() error {
	// The kernel process is long-lived and not bound to a single context;
	// its lifetime is managed via Close/respawn. We use context.Background()
	// so CommandContext does not kill the process prematurely.
	c := exec.CommandContext(context.Background(), k.cmd, k.args...) //nolint:gosec // trusted bootstrapForLang
	setPgid(c)
	stdin, err := c.StdinPipe()
	if err != nil {
		return fmt.Errorf("kernel: stdin pipe: %w", err)
	}
	stdoutPipe, err := c.StdoutPipe()
	if err != nil {
		return fmt.Errorf("kernel: stdout pipe: %w", err)
	}
	// Discard stderr: the bootstrap writes errors to stdout so the model sees
	// them. Interpreter-level stderr (e.g. startup warnings) would corrupt
	// sentinel detection.
	if err := c.Start(); err != nil {
		return fmt.Errorf("kernel: start %s: %w", k.cmd, err)
	}
	k.proc = c
	k.stdin = stdin
	k.stdout = bufio.NewReader(stdoutPipe)
	return nil
}

func (k *Kernel) kill() {
	if k.proc == nil || k.proc.Process == nil {
		return
	}
	_ = k.stdin.Close()
	_ = k.proc.Process.Kill()
	_ = k.proc.Wait()
	k.proc = nil
}

func (k *Kernel) respawn() error {
	k.kill()
	k.dirty = false
	return k.start()
}

// recoverDirty sends an empty code block to flush the bootstrap loop and
// drains stdout for up to 300 ms, looking for the sentinel.
func (k *Kernel) recoverDirty() error {
	sentinel := k.sentinel()
	// Send an empty code block. The bootstrap will exec("") and print the sentinel.
	payload := sentinel + "\n"
	if _, err := io.WriteString(k.stdin, payload); err != nil {
		return fmt.Errorf("kernel: drain write: %w", err)
	}
	// Drain until sentinel or 300 ms.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, peekErr := k.stdout.Peek(1); peekErr != nil {
			break
		}
		line, err := k.stdout.ReadString('\n')
		if strings.TrimRight(line, "\r\n") == sentinel {
			return nil
		}
		if err != nil {
			return fmt.Errorf("kernel: drain read: %w", err)
		}
	}
	return nil
}

func (k *Kernel) sentinel() string {
	return "__CONDUIT_EOT_" + k.nonce + "__"
}

// bootstrapForLang returns the command and args for the given language.
// The args embed a custom bootstrap loop so the interpreter does not use its
// built-in REPL (which emits prompts and banners that would corrupt our output).
func bootstrapForLang(lang, nonce string) (string, []string, error) {
	sentinel := "__CONDUIT_EOT_" + nonce + "__"
	switch lang {
	case "python":
		interp := "python3"
		if _, err := exec.LookPath("python3"); err != nil {
			interp = "python"
			if _, err2 := exec.LookPath("python"); err2 != nil {
				return "", nil, fmt.Errorf("kernel: python3/python not found on PATH")
			}
		}
		// The bootstrap reads lines from stdin. Lines before the sentinel are
		// buffered; when the sentinel line arrives the accumulated code is
		// executed in a shared globals dict and the sentinel is printed.
		// SIGINT (from timeout interruption) is caught and converted to
		// KeyboardInterrupt so the bootstrap loop survives.
		bootstrap := fmt.Sprintf(`
import sys, traceback, signal as _sig

def _sigint(signum, frame):
    raise KeyboardInterrupt()
_sig.signal(_sig.SIGINT, _sigint)

_g = {"__builtins__": __builtins__}
_s = %q
_buf = []
for _line in sys.stdin:
    _line = _line.rstrip('\n')
    if _line == _s:
        _code = '\n'.join(_buf)
        _buf = []
        try:
            exec(compile(_code, '<conduit>', 'exec'), _g)
        except KeyboardInterrupt:
            sys.stdout.write('KeyboardInterrupt\n')
        except Exception:
            traceback.print_exc(file=sys.stdout)
        sys.stdout.write(_s + '\n')
        sys.stdout.flush()
    else:
        _buf.append(_line)
`, sentinel)
		// -u: unbuffered I/O so lines are flushed immediately.
		return interp, []string{"-u", "-c", bootstrap}, nil

	case "node":
		if _, err := exec.LookPath("node"); err != nil {
			return "", nil, fmt.Errorf("kernel: node not found on PATH")
		}
		// The bootstrap uses Node's vm module for a persistent context and
		// readline for line-by-line stdin processing. Code is run directly via
		// vm.runInContext so that top-level var declarations persist across calls.
		// SIGINT is caught to keep the process alive.
		bootstrap := fmt.Sprintf(`
const readline = require('readline');
const vm = require('vm');
const sentinel = %q;
const ctx = vm.createContext({ require, console, process, Promise, Buffer, setTimeout, setInterval, clearTimeout, clearInterval });
const rl = readline.createInterface({ input: process.stdin, terminal: false, crlfDelay: Infinity });
let buf = [];

process.on('SIGINT', () => {});

rl.on('line', (line) => {
  if (line === sentinel) {
    const code = buf.join('\n');
    buf = [];
    (async () => {
      try {
        const result = vm.runInContext(code, ctx, { filename: '<conduit>', displayErrors: false });
        if (result != null && typeof result.then === 'function') await result;
      } catch(e) {
        process.stdout.write((e && e.message ? e.message : String(e)) + '\n');
      }
      process.stdout.write(sentinel + '\n');
    })();
  } else {
    buf.push(line);
  }
});
`, sentinel)
		return "node", []string{"-e", bootstrap}, nil

	default:
		return "", nil, fmt.Errorf("kernel: unsupported language %q", lang)
	}
}

func randomNonce() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
