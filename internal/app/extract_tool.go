package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const archiveListingLimit = 64 * 1024 * 1024

type archiveTool struct{ executable, format string }

type archiveMember struct {
	name      string
	relative  string
	size      int64
	directory bool
	crc       string
	modified  time.Time
}

func findArchiveTool() (archiveTool, error) {
	for _, name := range []string{"7zz", "7z"} {
		if executable, err := exec.LookPath(name); err == nil {
			return archiveTool{executable: executable}, nil
		}
	}
	if runtime.GOOS == "windows" {
		for _, variable := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
			if base := os.Getenv(variable); base != "" {
				if executable, err := exec.LookPath(filepath.Join(base, "7-Zip", "7z.exe")); err == nil {
					return archiveTool{executable: executable}, nil
				}
			}
		}
	}
	return archiveTool{}, fmt.Errorf("extract mode requires 7-Zip (7zz or 7z); the Docker image includes it")
}

type limitedArchiveBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (buffer *limitedArchiveBuffer) Write(data []byte) (int, error) {
	length := len(data)
	remaining := archiveListingLimit - buffer.Len()
	if len(data) > remaining {
		data = data[:remaining]
		buffer.exceeded = true
	}
	_, _ = buffer.Buffer.Write(data)
	return length, nil // Drain the process even after the memory limit is hit.
}

func (tool archiveTool) command(ctx context.Context, command, archive, password string, extra ...string) *exec.Cmd {
	args := []string{command, "-y", "-bd", "-bb0", "-sccUTF-8", "-ssc", "-spd", "-r-"}
	if password != "" {
		args = append(args, "-p"+password)
	}
	if tool.format != "" {
		args = append(args, "-t"+tool.format)
	}
	args = append(args, extra...)
	args = append(args, "--", archive)
	cmd := exec.CommandContext(ctx, tool.executable, args...)
	if runtime.GOOS != "windows" {
		// Technical listings omit a timezone. Use UTC in the child so dates
		// survive local timezone and daylight-saving transitions unchanged.
		cmd.Env = append(os.Environ(), "TZ=UTC")
	}
	cmd.Stdin = strings.NewReader("") // EOF prevents interactive password prompts.
	cmd.Stderr = io.Discard           // Tool diagnostics can contain password or hostile filename text.
	return cmd
}

func archiveCommandError(ctx context.Context, command string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("7-Zip %s failed with exit code %d; original archive retained", command, exit.ExitCode())
	}
	return fmt.Errorf("cannot run 7-Zip %s; original archive retained", command)
}

func (tool archiveTool) password(ctx context.Context, archive string, passwords []string) (string, error) {
	attempts := []string{""}
	seen := map[string]bool{"": true}
	for _, password := range passwords {
		if !seen[password] {
			attempts = append(attempts, password)
			seen[password] = true
		}
	}
	for _, password := range attempts {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		cmd := tool.command(ctx, "t", archive, password)
		cmd.Stdout = io.Discard
		err := cmd.Run()
		if err == nil {
			return password, nil
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || (exit.ExitCode() != 2 && !(password == "" && exit.ExitCode() == 255 && ctx.Err() == nil)) {
			// Warnings, invocation failures, cancellation and all other exit
			// codes must never authorize archive deletion or password retries.
			return "", archiveCommandError(ctx, "test", err)
		}
	}
	return "", fmt.Errorf("archive is corrupt, unsupported, or no supplied password succeeded; original archive retained")
}

func (tool archiveTool) list(ctx context.Context, archive, password string, selector ...string) ([]archiveMember, error) {
	cmd := tool.command(ctx, "l", archive, password, "-slt", "-ba")
	cmd.Args = append(cmd.Args, selector...)
	var output limitedArchiveBuffer
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return nil, archiveCommandError(ctx, "list", err)
	}
	if output.exceeded {
		return nil, fmt.Errorf("archive listing exceeds the 64 MiB safety limit")
	}
	return parseArchiveListing(output.String())
}

func parseArchiveListing(listing string) ([]archiveMember, error) {
	if !utf8.ValidString(listing) {
		return nil, fmt.Errorf("archive filenames are not valid UTF-8")
	}
	listing = strings.ReplaceAll(listing, "\r\n", "\n")
	var members []archiveMember
	seen := make(map[string]bool)
	for _, block := range strings.Split(strings.Trim(listing, "\n"), "\n\n") {
		if block == "" {
			continue
		}
		fields := make(map[string]string)
		for _, line := range strings.Split(block, "\n") {
			key, value, ok := strings.Cut(line, " = ")
			if !ok || key == "" {
				return nil, fmt.Errorf("unrecognized archive listing; original archive retained")
			}
			if _, exists := fields[key]; exists {
				return nil, fmt.Errorf("ambiguous archive listing; original archive retained")
			}
			fields[key] = value
		}
		name, hasName := fields["Path"]
		if !hasName {
			return nil, fmt.Errorf("archive entry has no path")
		}
		relative, err := safeArchivePath(name)
		if err != nil {
			return nil, err
		}
		key := relative
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if seen[key] {
			return nil, fmt.Errorf("archive contains duplicate or conflicting paths: %q", relative)
		}
		seen[key] = true
		attributes := fields["Attributes"]
		if fields["NT Security"] != "" {
			return nil, fmt.Errorf("archive contains security descriptors that cannot be preserved: %q", relative)
		}
		for _, value := range strings.Fields(attributes) {
			if len(value) >= 10 && strings.ContainsRune("lspbc", rune(value[0])) {
				return nil, fmt.Errorf("archive contains a link or special file: %q", relative)
			}
		}
		for key, value := range fields {
			if key == "Links" && (value == "1" || value == "0") {
				continue
			}
			if value != "" && value != "-" && (strings.Contains(strings.ToLower(key), "link") || strings.Contains(strings.ToLower(key), "reparse") || key == "Alternate Stream" || key == "Alternate Streams" || key == "Anti") {
				return nil, fmt.Errorf("archive contains unsupported links, streams, or anti-items: %q", relative)
			}
		}
		directory := fields["Folder"] == "+" || strings.HasPrefix(attributes, "D")
		if relative == "." && !directory {
			return nil, fmt.Errorf("archive root entry must be a directory")
		}
		size := int64(0)
		if !directory {
			value, ok := fields["Size"]
			if !ok {
				return nil, fmt.Errorf("archive entry has no size: %q", relative)
			}
			size, err = strconv.ParseInt(value, 10, 64)
			if err != nil || size < 0 {
				return nil, fmt.Errorf("invalid archive entry size: %q", relative)
			}
		}
		var modified time.Time
		if value := fields["Modified"]; value != "" {
			zone := time.UTC
			if runtime.GOOS == "windows" {
				zone = time.Local // Windows 7-Zip uses the system timezone.
			}
			modified, err = parseArchiveModified(value, zone, runtime.GOOS == "windows")
			if err != nil {
				return nil, fmt.Errorf("archive modification date cannot be preserved: %q", relative)
			}
		}
		members = append(members, archiveMember{name: name, relative: relative, size: size, directory: directory, crc: fields["CRC"], modified: modified})
	}
	kinds := make(map[string]bool)
	for _, member := range members {
		key := member.relative
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		kinds[key] = member.directory
	}
	for _, member := range members {
		for parent := path.Dir(member.relative); parent != "."; parent = path.Dir(parent) {
			key := parent
			if runtime.GOOS == "windows" {
				key = strings.ToUpper(key)
			}
			if directory, exists := kinds[key]; exists && !directory {
				return nil, fmt.Errorf("archive path is both a file and a parent directory: %q", parent)
			}
		}
	}
	return members, nil
}

func validateArchiveTimestamp(stamp time.Time) error {
	if !time.Unix(0, stamp.UnixNano()).Equal(stamp) {
		return fmt.Errorf("archive timestamp is outside the supported nanosecond range")
	}
	return nil
}

func parseArchiveModified(value string, zone *time.Location, local bool) (time.Time, error) {
	const layout = "2006-01-02 15:04:05.999999999"
	stamp, err := time.ParseInLocation(layout, value, zone)
	if err != nil {
		return time.Time{}, err
	}
	wall, err := time.Parse(layout, value)
	if err != nil || stamp.Format(layout) != wall.Format(layout) {
		return time.Time{}, fmt.Errorf("archive timestamp does not exist in the system timezone")
	}
	if err := validateArchiveTimestamp(stamp); err != nil {
		return time.Time{}, err
	}
	if local {
		for _, shift := range []time.Duration{-2 * time.Hour, -time.Hour, -30 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour} {
			if stamp.Add(shift).Format(layout) == stamp.Format(layout) {
				return time.Time{}, fmt.Errorf("archive timestamp is ambiguous in the system timezone")
			}
		}
	}
	return stamp, nil
}

func safeArchivePath(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return "", fmt.Errorf("unsafe archive path: %q", name)
	}
	for _, value := range name {
		if unicode.IsControl(value) {
			return "", fmt.Errorf("control character in archive path")
		}
	}
	name = strings.TrimSuffix(name, "/")
	for strings.HasPrefix(name, "./") {
		name = strings.TrimPrefix(name, "./")
	}
	if name == "." || name == "" {
		return ".", nil
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." || len(component) > 255 {
			return "", fmt.Errorf("unsafe archive path component: %q", component)
		}
		if strings.TrimRight(component, " .") != component {
			return "", fmt.Errorf("ambiguous trailing whitespace or dot in archive path: %q", component)
		}
		base := strings.ToUpper(strings.SplitN(component, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9') {
			return "", fmt.Errorf("reserved device name in archive: %q", component)
		}
	}
	return name, nil
}

func (tool archiveTool) verifySelectors(ctx context.Context, archive, password string, members []archiveMember) error {
	for _, expected := range members {
		selected, err := tool.list(ctx, archive, password, expected.name)
		if err != nil {
			return err
		}
		matched := false
		for _, actual := range selected {
			if actual.relative == expected.relative && actual.directory == expected.directory && actual.size == expected.size {
				matched = true
			}
		}
		if !matched {
			// 7-Zip replaces control characters when displaying names. Those
			// rendered names must not silently replace the originals on disk.
			return fmt.Errorf("archive filename cannot be represented unambiguously: %q", expected.relative)
		}
	}
	return nil
}
