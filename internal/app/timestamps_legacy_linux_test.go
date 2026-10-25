//go:build linux && (amd64 || arm64)

package app

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestLinuxLegacyKernelTimestamps(t *testing.T) {
	requireArchiveTool(t)
	if os.Getenv("DEDUP_TEST_LEGACY_TIMESTAMPS") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command(executable, "-test.run=^TestLinuxLegacyKernelTimestamps$", "-test.v")
		command.Env = append(os.Environ(), "DEDUP_TEST_LEGACY_TIMESTAMPS=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("older-kernel timestamp regression failed: %v\n%s", err, output)
		}
		return
	}

	// Restrict only this isolated subprocess's test thread. Emulate Linux <5.8
	// by returning EINVAL for utimensat's AT_EMPTY_PATH flag, while allowing the
	// original NULL-path futimens form and every other filesystem operation.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0}, // seccomp_data.nr
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(unix.SYS_UTIMENSAT), Jf: 3},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16 + 3*8}, // args[3]: flags
		{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: unix.AT_EMPTY_PATH, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: 0x00050000 | uint32(unix.EINVAL)}, // SECCOMP_RET_ERRNO
		{Code: unix.BPF_RET | unix.BPF_K, K: 0x7fff0000},                       // SECCOMP_RET_ALLOW
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, errno := unix.Syscall6(unix.SYS_PRCTL, unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0, 0); errno != 0 {
		t.Fatal(errno)
	}

	root := t.TempDir()
	file, err := os.Create(filepath.Join(root, "timestamps"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	stamp := time.Unix(1702626238, 123456789)
	times := []unix.Timespec{unix.NsecToTimespec(stamp.UnixNano()), unix.NsecToTimespec(stamp.UnixNano())}
	if err := unix.UtimesNanoAt(int(file.Fd()), "", times, unix.AT_EMPTY_PATH); !errors.Is(err, unix.EINVAL) {
		t.Fatalf("did not reproduce the original older-kernel failure: %v", err)
	}
	if err := setFileTimes(file, stamp); err != nil {
		t.Fatal(err)
	}
	if info, err := file.Stat(); err != nil || !info.ModTime().Equal(stamp) {
		t.Fatalf("descriptor timestamps lost nanosecond precision: %v, %v", info, err)
	}
	TestSafetyLinuxTimestampsUseDescriptor(t)

	// Exercise complete extraction, including original removal, under the same
	// rejected flag that stopped ZIP extraction on the reported NAS kernel.
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for _, name := range []string{"payload.txt", "nested/second.txt"} {
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: stamp.Truncate(time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, "archive content"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archives := t.TempDir()
	if err := os.WriteFile(filepath.Join(archives, "#158.zip"), data.Bytes(), 0640); err != nil {
		t.Fatal(err)
	}
	if err := run(testConfig(archives, ModeExtract), io.Discard); err != nil {
		t.Fatal(err)
	}
	assertLayout(t, archives, map[string]string{"#158/payload.txt": "archive content", "#158/nested/second.txt": "archive content"})

	// Cross-filesystem moves share the timestamp helper, so test their complete
	// copy-and-verify path as well, without needing an additional mounted disk.
	copies := t.TempDir()
	writeFixture(t, copies, map[string]string{"source": "copied content"})
	if err := os.Chtimes(filepath.Join(copies, "source"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	result, err := copyMove(recordFile(t, filepath.Join(copies, "source")), filepath.Join(copies, "destination"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.info.ModTime().Equal(stamp) {
		t.Fatal("copied timestamp changed")
	}
	assertLayout(t, copies, map[string]string{"destination": "copied content"})
}
