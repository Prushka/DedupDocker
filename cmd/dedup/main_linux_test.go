//go:build linux

package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSIGTERMStopsPlanningCleanly(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "dedup")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	root := t.TempDir()
	const size = 512 * 1024 * 1024
	for _, name := range []string{"a", "b"} {
		file, err := os.Create(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		truncateErr, closeErr := file.Truncate(size), file.Close()
		if err := errors.Join(truncateErr, closeErr); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary)
	command.Env = append(commandEnvironment(), "ROOT="+root, "DRY_RUN=false")
	output, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || !strings.Contains(scanner.Text(), "Planning") {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal("CLI did not announce planning")
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal(err)
	}
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	err = command.Wait()
	var exit *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(strings.Join(lines, "\n"), "context canceled") {
		t.Fatalf("SIGTERM did not unwind cleanly: %v\n%s", err, strings.Join(lines, "\n"))
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 {
		t.Fatalf("signal left staged files or deleted sources: %v, %v", entries, err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.Size() != size {
			t.Fatalf("signal changed source: %v, %v", info, err)
		}
	}
}
