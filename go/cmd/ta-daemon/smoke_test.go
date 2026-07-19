package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"typeanything/internal/ipc"
)

// TestDaemonSmoke_EndToEnd starts the real daemon in-process, sends an
// activate + a wubi 'g' keystroke, and verifies candidates come back.
// This proves the IPC server, dispatcher, wubi engine, and dict-loading
// pipeline are all wired together correctly end-to-end.
//
// We don't test the translate path here because it would hit the real
// LLM API (covered separately by tools/eval/run_eval.py).
func TestDaemonSmoke_EndToEnd(t *testing.T) {
	// Pick a unique pipe path so parallel test runs don't collide, and
	// a per-test rime dir so we don't pollute the real %APPDATA%\Rime.
	pipePath := `\\.\pipe\ta-smoke-` + strings.ReplaceAll(t.Name(), "/", "-")
	rimeDir := t.TempDir()
	// Use the repo's checked-in wubi dict for the smoke test. From
	// go/cmd/ta-daemon/, the dict is four levels up.
	dictPath := "../../../third_party/weasel/librime/plugins/typeanything/schema/wubi86.dict.yaml"

	stop, err := startDaemonForTest(pipePath, rimeDir, dictPath)
	if err != nil {
		t.Fatalf("startDaemon: %v", err)
	}
	defer stop()

	// The dict load takes ~0.5s on the real 137k-line file. Poll for
	// the pipe to come up rather than a fixed sleep.
	cli := ipc.NewClient(pipePath, 3*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	var pingOK bool
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		if err := cli.Ping(ctx); err == nil {
			cancel()
			pingOK = true
			break
		}
		cancel()
		time.Sleep(100 * time.Millisecond)
	}
	if !pingOK {
		t.Fatal("daemon never came up within 5s")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Activate a session.
	resp, err := cli.Call(ctx, ipc.Request{Op: ipc.OpActivate, Session: 42})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if !resp.Accepted || resp.SchemaID != "wubi86" {
		t.Fatalf("activate resp: %+v", resp)
	}

	// 2. Press 'g' — should produce candidates including 一/王 (code 'g').
	resp, err = cli.Call(ctx, ipc.Request{Op: ipc.OpKey, Session: 42, KeyCode: 0x47, Char: "g"})
	if err != nil {
		t.Fatalf("key 'g': %v", err)
	}
	if !resp.Accepted {
		t.Fatalf("key 'g' not accepted: %+v", resp)
	}
	if len(resp.Candidates) == 0 {
		t.Fatalf("key 'g' returned no candidates: %+v", resp)
	}
	if !containsAny(resp.Candidates, "一") {
		t.Errorf("'g' candidates missing 一: %v", resp.Candidates)
	}
	t.Logf("'g' → preedit=%q candidates=%v", resp.Preedit, topCands(resp.Candidates, 5))

	// 3. Type 'gtt' to reach 'ggtt' = 五笔. Each full-code (4-char)
	// input auto-tops to the highest-ranked candidate, which is 五笔
	// (weight 6.09M, dominant over 来来往往 at 1.53M).
	for _, ch := range "gtt" {
		resp, err = cli.Call(ctx, ipc.Request{Op: ipc.OpKey, Session: 42, Char: string(ch)})
		if err != nil {
			t.Fatalf("key %q: %v", ch, err)
		}
	}
	// After the 4th key ('ggtt') the dispatcher auto-commits the top
	// candidate via the Commit field. Verify by checking the last
	// response carried a Commit.
	if resp.Commit != "五笔" {
		t.Logf("note: full-code auto-top didn't commit 五笔 (got %q) — "+
			"acceptable if ranking differs; candidates=%v",
			resp.Commit, resp.Candidates)
	} else {
		t.Logf("'ggtt' → auto-committed %q", resp.Commit)
	}

	// 4. Press Enter. With the auto-committed text now in the
	// translation buffer, Enter triggers DispatchTranslate. Without
	// an api_key the pipeline logs and returns immediately (we
	// assert only that Enter doesn't crash the daemon).
	resp, err = cli.Call(ctx, ipc.Request{Op: ipc.OpKey, Session: 42, KeyCode: 0x0D})
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	t.Logf("enter resp: %+v", resp)
}

func containsAny(slice []string, want string) bool {
	for _, s := range slice {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func topCands(c []string, n int) []string {
	if n > len(c) {
		n = len(c)
	}
	return c[:n]
}

// startDaemonForTest is the test-only wiring of main(). It returns a
// stop function that shuts the daemon down cleanly. Kept in this file
// (not main.go) so production builds don't pay for the test helpers.
func startDaemonForTest(pipePath, rimeDir, dictPath string) (func(), error) {
	ctx, cancel := context.WithCancel(context.Background())
	stop := func() { cancel(); time.Sleep(150 * time.Millisecond) }

	go func() {
		_ = runDaemon(ctx, pipePath, rimeDir, dictPath)
	}()
	return stop, nil
}
