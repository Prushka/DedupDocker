//go:build linux

package app

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxCopyRefusesInheritedDestinationACL(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source": "private source data"}, "destination")
	// POSIX ACL version 2: an additional named user plus the required entries.
	acl := make([]byte, 4)
	binary.LittleEndian.PutUint32(acl, 2)
	for _, entry := range []struct {
		tag, permissions uint16
		id               uint32
	}{{1, 7, ^uint32(0)}, {2, 4, 12345}, {4, 0, ^uint32(0)}, {16, 4, ^uint32(0)}, {32, 0, ^uint32(0)}} {
		acl = binary.LittleEndian.AppendUint16(acl, entry.tag)
		acl = binary.LittleEndian.AppendUint16(acl, entry.permissions)
		acl = binary.LittleEndian.AppendUint32(acl, entry.id)
	}
	destination := filepath.Join(root, "destination")
	if err := unix.Setxattr(destination, "system.posix_acl_default", acl, 0); err != nil {
		t.Skipf("default ACLs unavailable: %v", err)
	}
	if _, err := copyMove(recordFile(t, filepath.Join(root, "source")), filepath.Join(destination, "file")); err == nil {
		t.Fatal("published a copy with an inherited ACL")
	}
	assertLayout(t, root, map[string]string{"source": "private source data"}, "destination")
}

func TestLinuxMountInfoEscapedPaths(t *testing.T) {
	data := "41 1 0:2 / /album\\040name\\011with\\012newline\\134slash rw - tmpfs tmpfs rw\n"
	if !mountInfoContains(data, "/album name\twith\nnewline\\slash") || mountInfoContains(data, "/album") {
		t.Fatal("mountinfo escaped paths were not matched exactly")
	}
}

func requireLinuxMountTests(t *testing.T) {
	t.Helper()
	if os.Getenv("DEDUP_MOUNT_TESTS") != "1" {
		t.Skip("requires the isolated mount-namespace CI check")
	}
}

func mountAuditFixture(t *testing.T, source, destination, kind string, flags uintptr) {
	t.Helper()
	if err := unix.Mount(source, destination, kind, flags, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Unmount(destination, 0); err != nil {
			t.Errorf("unmount fixture: %v", err)
		}
	})
}

func TestLinuxIntegrationFlattenAcrossMountPreservesOwnership(t *testing.T) {
	requireLinuxMountTests(t)
	root := t.TempDir()
	writeFixture(t, root, nil, "child/mounted")
	mounted := filepath.Join(root, "child", "mounted")
	mountAuditFixture(t, "tmpfs", mounted, "tmpfs", 0)
	writeFixture(t, mounted, map[string]string{"file.txt": "owned file from another filesystem"})
	source := filepath.Join(mounted, "file.txt")
	if err := os.Chown(source, 12345, 12346); err != nil {
		t.Fatal(err)
	}
	before := recordFile(t, source)
	config := testConfig(root, ModeFlatten)
	config.DryRun = true
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := before.verify(); err != nil {
		t.Fatal(err)
	}
	config.DryRun = false
	if err := run(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	after := recordFile(t, filepath.Join(root, "child", "file.txt"))
	if !sameOwnership(before.info, after.info) || before.info.Mode() != after.info.Mode() || !before.info.ModTime().Equal(after.info.ModTime()) {
		t.Fatal("cross-filesystem move changed ownership, permissions, or modification time")
	}
	assertLayout(t, root, map[string]string{"child/file.txt": "owned file from another filesystem"}, "child/mounted")
}

func TestLinuxIntegrationEmptyMountsRemain(t *testing.T) {
	requireLinuxMountTests(t)
	for _, mode := range []string{ModeDedup, ModeFlatten} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, nil, "child/parent/mounted", "child/empty")
			mountAuditFixture(t, "tmpfs", filepath.Join(root, "child", "parent", "mounted"), "tmpfs", 0)
			config := testConfig(root, mode)
			config.EmptyDir = true
			if err := run(config, io.Discard); err != nil {
				t.Fatal(err)
			}
			assertLayout(t, root, nil, "child/parent/mounted")
		})
	}
}

func TestLinuxIntegrationBindAliasesAbortBeforeEdits(t *testing.T) {
	requireLinuxMountTests(t)
	for _, cycle := range []bool{false, true} {
		t.Run(map[bool]string{false: "overlapping-roots", true: "cycle"}[cycle], func(t *testing.T) {
			base := t.TempDir()
			root, alias := filepath.Join(base, "root"), filepath.Join(base, "alias")
			writeFixture(t, root, map[string]string{"sub/delete-me": "preserve"})
			if cycle {
				alias = filepath.Join(root, "sub", "alias")
			}
			if err := os.MkdirAll(alias, 0755); err != nil {
				t.Fatal(err)
			}
			mountAuditFixture(t, filepath.Join(root, "sub"), alias, "", unix.MS_BIND)
			config := testConfig(root, ModeDeleteFilesIncluding)
			config.DeleteFilesIncluding = []string{"delete-me"}
			if !cycle {
				config.Roots = append(config.Roots, alias)
			}
			if err := run(config, io.Discard); err == nil {
				t.Fatal("accepted overlapping directory identities through a bind mount")
			}
			if data, err := os.ReadFile(filepath.Join(root, "sub", "delete-me")); err != nil || string(data) != "preserve" {
				t.Fatalf("bind alias validation allowed file edits: %q, %v", data, err)
			}
		})
	}
}
