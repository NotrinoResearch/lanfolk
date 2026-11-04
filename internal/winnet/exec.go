package winnet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// commandTimeout bounds every external command.
const commandTimeout = 30 * time.Second

// Command is an external program invocation. It never goes through a shell:
// Args are passed to the program as separate arguments. Env holds extra
// KEY=VALUE entries that are added to the inherited environment; values that
// come from outside the package travel through Env, never through the script
// text.
type Command struct {
	Name string
	Args []string
	Env  []string
}

func (c Command) String() string {
	return c.Name + " " + strings.Join(c.Args, " ")
}

// runner executes a command and returns its combined output. Tests replace it.
var runner = runCommand

func runCommand(ctx context.Context, c Command) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	hideWindow(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("timed out after %v", commandTimeout)
		}
		return out.Bytes(), fmt.Errorf("winnet: %s: %w: %s", c.Name, err, trimOutput(out.Bytes()))
	}
	return out.Bytes(), nil
}

func trimOutput(b []byte) string {
	s := strings.TrimSpace(string(b))
	const max = 1024
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}

func run(ctx context.Context, c Command) error {
	_, err := runner(ctx, c)
	return err
}

// psQuote quotes s as a PowerShell single-quoted string literal. Only
// package-controlled constants are quoted; external values use Env.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// powershellCommand builds a Windows PowerShell invocation of script. The
// script must not contain double quotes, so no argument escaping is needed.
func powershellCommand(script string, env ...string) Command {
	return Command{
		Name: powershellExe(),
		Args: []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "$ErrorActionPreference = 'Stop'; " + script},
		Env:  env,
	}
}
