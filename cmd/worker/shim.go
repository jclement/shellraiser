package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/jclement/shellraiser/internal/cmdrelay"
)

// The cmd-shim runs inside a worker container as a stand-in for a CLI tool the
// device exposes (op, gh, …). Most invocations are forwarded verbatim to the
// device. `op run` / `op inject` are special-cased: they would otherwise execute
// an arbitrary command (or write files) on the DEVICE — so instead we resolve
// only the secrets on the device (via `op read`) and run the command / write the
// file here in the container, where danger-mode already lives.

// runShim dispatches a forwarded command invocation. Returns the process exit code.
func runShim(name string, argv []string) int {
	if name == "" {
		fmt.Fprintln(os.Stderr, "shellraiser: cmd-shim: missing command name")
		return 2
	}
	if name == "op" && len(argv) > 0 {
		switch argv[0] {
		case "run":
			return opRun(argv[1:])
		case "inject":
			return opInject(argv[1:])
		}
	}
	return relay(name, argv, os.Stdin, os.Stdout, os.Stderr)
}

// relay forwards one invocation to the device over the command-relay socket.
func relay(name string, argv []string, in io.Reader, out, errw io.Writer) int {
	conn, err := dialRelay()
	if err != nil {
		fmt.Fprintln(errw, "shellraiser:", err)
		return 127
	}
	return cmdrelay.Shim(conn, name, argv, curatedEnv(), in, out, errw)
}

// relayCapture forwards an invocation and returns its stdout (for secret
// resolution). stderr passes through.
func relayCapture(name string, argv []string) (string, int) {
	var buf bytes.Buffer
	code := relay(name, argv, strings.NewReader(""), &buf, os.Stderr)
	return strings.TrimRight(buf.String(), "\r\n"), code
}

func dialRelay() (net.Conn, error) {
	sock := os.Getenv("SHELLRAISER_CMD_SOCK")
	if sock == "" {
		return nil, fmt.Errorf("no device command relay (SHELLRAISER_CMD_SOCK unset)")
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("no device connected to run this command: %v", err)
	}
	return c, nil
}

func curatedEnv() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	return env
}

var opRef = regexp.MustCompile(`op://[A-Za-z0-9._\-/]+`)

// opRun implements `op run [--env-file F] -- cmd args…`: resolve op:// secret
// references (in the environment and any --env-file) on the device, then exec the
// command HERE with the resolved values. The device only ever runs `op read`.
func opRun(args []string) int {
	var envFiles []string
	var cmd []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			cmd = args[i+1:]
			i = len(args)
		case a == "--env-file" && i+1 < len(args):
			i++
			envFiles = append(envFiles, args[i])
		case strings.HasPrefix(a, "--env-file="):
			envFiles = append(envFiles, strings.TrimPrefix(a, "--env-file="))
		case a == "--no-masking" || a == "--":
			// no-ops for our local-exec model
		}
	}
	if len(cmd) == 0 {
		fmt.Fprintln(os.Stderr, "shellraiser: op run: expected `-- command` (the command runs in the container; only secrets resolve on the device)")
		return 2
	}

	// Start from the current environment, layer in any --env-file entries.
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	for _, f := range envFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "shellraiser: op run: %v\n", err)
			return 1
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if i := strings.IndexByte(line, '='); i > 0 {
				env[line[:i]] = strings.Trim(line[i+1:], `"'`)
			}
		}
	}

	// Resolve every op:// reference on the device, caching repeats.
	cache := map[string]string{}
	for k, v := range env {
		if !strings.HasPrefix(v, "op://") {
			continue
		}
		resolved, ok := cache[v]
		if !ok {
			out, code := relayCapture("op", []string{"read", v})
			if code != 0 {
				fmt.Fprintf(os.Stderr, "shellraiser: op run: could not resolve %s\n", v)
				return code
			}
			resolved = out
			cache[v] = resolved
		}
		env[k] = resolved
	}

	bin, err := exec.LookPath(cmd[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "shellraiser: op run: %v\n", err)
		return 127
	}
	c := exec.Command(bin, cmd[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	c.Env = flatten(env)
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "shellraiser: op run: %v\n", err)
		return 1
	}
	return 0
}

// opInject implements `op inject [-i in] [-o out]`: resolve op:// references in
// the template on the device and write the rendered result HERE.
func opInject(args []string) int {
	inPath, outPath := "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-i", "--in-file":
			if i+1 < len(args) {
				i++
				inPath = args[i]
			}
		case "-o", "--out-file":
			if i+1 < len(args) {
				i++
				outPath = args[i]
			}
		}
	}
	var tmpl []byte
	var err error
	if inPath == "" {
		tmpl, err = io.ReadAll(os.Stdin)
	} else {
		tmpl, err = os.ReadFile(inPath)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "shellraiser: op inject: %v\n", err)
		return 1
	}
	cache := map[string]string{}
	failed := false
	rendered := opRef.ReplaceAllStringFunc(string(tmpl), func(ref string) string {
		if v, ok := cache[ref]; ok {
			return v
		}
		out, code := relayCapture("op", []string{"read", ref})
		if code != 0 {
			failed = true
			return ref
		}
		cache[ref] = out
		return out
	})
	if failed {
		fmt.Fprintln(os.Stderr, "shellraiser: op inject: could not resolve one or more references")
		return 1
	}
	if outPath == "" {
		_, _ = os.Stdout.WriteString(rendered)
	} else if err := os.WriteFile(outPath, []byte(rendered), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "shellraiser: op inject: %v\n", err)
		return 1
	}
	return 0
}

func flatten(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
