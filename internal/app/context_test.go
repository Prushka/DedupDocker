package app

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCanceledPlanNeverEdits(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"delete-me": "preserve"})
	before := snapshot(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	config := testConfig(root, ModeDeleteFilesIncluding)
	config.DeleteFilesIncluding = []string{"delete-me"}
	if err := runContext(ctx, config, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Fatal("canceled plan changed files")
	}
}

func TestCancellationRestoresCapturedKeeper(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{"source": "same content", "keeper": "same content"}
	writeFixture(t, root, files)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keeper := recordFile(t, filepath.Join(root, "keeper"))
	keeper.ctx = ctx
	captured, err := captureFile(keeper, false, true)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, compareErr := sameContents(recordFile(t, filepath.Join(root, "source")), captured.record())
	closeErr := captured.close()
	if !errors.Is(compareErr, context.Canceled) || closeErr != nil {
		t.Fatalf("captured comparison cancellation: %v; restore: %v", compareErr, closeErr)
	}
	assertLayout(t, root, files)
}
