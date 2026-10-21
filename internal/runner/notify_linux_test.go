// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package runner

import (
	"crypto/rand"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRunNotificationSenderIsWrapper(t *testing.T) {
	for _, leased := range []bool{false, true} {
		name := "unleased"
		if leased {
			name = "leased"
		}
		t.Run(name, func(t *testing.T) { testRunNotification(t, leased, false) })
	}
}

func TestRunNotifyCleanupFailure(t *testing.T) {
	if unix.Geteuid() == 0 {
		t.Skip("directory permission refusal requires an unprivileged user")
	}
	testRunNotification(t, false, true)
}

func testRunNotification(t *testing.T, leased, denyCleanup bool) {
	managerPath := filepath.Join(t.TempDir(), "notify")
	manager, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: managerPath, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	raw, err := manager.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var optionErr error
	if err := raw.Control(func(fd uintptr) {
		optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1)
	}); err != nil || optionErr != nil {
		t.Fatalf("receive sender credentials: %v, %v", err, optionErr)
	}
	t.Setenv("NOTIFY_SOCKET", managerPath)
	t.Setenv("BERTH_NOTIFY_CHILD", "1")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if denyCleanup {
		t.Setenv("BERTH_NOTIFY_DENY_CLEANUP", "1")
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		t.Cleanup(func() {
			dirs, err := filepath.Glob(filepath.Join(runtimeDir, "berth-notify-*"))
			if err != nil {
				t.Error(err)
			}
			for _, dir := range dirs {
				if err := os.Chmod(dir, 0700); err != nil {
					t.Error(err)
				}
				if err := os.RemoveAll(dir); err != nil {
					t.Error(err)
				}
			}
		})
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exited := make(chan int, 1)
	socket := filepath.Join(t.TempDir(), "absent")
	if leased {
		socket, _ = parkDaemon(t, &parkCard{total: 24 << 30}, 16<<30)
	}
	go func() {
		exited <- Run(Options{Socket: socket, Engine: "notify-fixture", Priority: "interactive",
			Argv: []string{executable, "-test.run=^TestNotifyChild$"}, Stderr: io.Discard})
	}()
	if err := manager.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var sender int32
	for _, want := range []string{"READY=1\nSTATUS=child ready", "BARRIER=1"} {
		data, control := make([]byte, 4096), make([]byte, 4096)
		n, oobn, _, _, err := manager.ReadMsgUnix(data, control)
		if err != nil {
			t.Fatal(err)
		}
		messages, err := unix.ParseSocketControlMessage(control[:oobn])
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range messages {
			switch message.Header.Type {
			case unix.SCM_CREDENTIALS:
				credentials, err := unix.ParseUnixCredentials(&message)
				if err != nil {
					t.Fatal(err)
				}
				sender = credentials.Pid
			case unix.SCM_RIGHTS:
				fds, err := unix.ParseUnixRights(&message)
				if err != nil {
					t.Fatal(err)
				}
				for _, fd := range fds {
					_ = unix.Close(fd)
				}
			}
		}
		if string(data[:n]) != want {
			t.Errorf("notification = %q, want %q", data[:n], want)
		}
	}
	select {
	case code := <-exited:
		want := 0
		if denyCleanup {
			want = 1
		}
		if code != want {
			t.Errorf("runner exit = %d, want %d", code, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child barrier did not finish")
	}
	if sender != int32(os.Getpid()) {
		t.Fatalf("notification sender = %d, want wrapper %d; a scoped child cannot ready the wrapper's service", sender, os.Getpid())
	}
}

func closeNotify(t *testing.T, n *notifications) {
	t.Helper()
	if err := n.close(); err != nil {
		t.Error(err)
	}
}

func notifySocket(n *notifications) string {
	for _, entry := range n.env {
		if value, ok := strings.CutPrefix(entry, "NOTIFY_SOCKET="); ok {
			return value
		}
	}
	return ""
}

func notifyManager(t *testing.T, abstract bool) (*net.UnixConn, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manager")
	if abstract {
		path = "@berth-notify-test-" + rand.Text()
	}
	manager, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close() })
	t.Setenv("NOTIFY_SOCKET", path)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	return manager, path
}

func TestNotifySetupAndCleanup(t *testing.T) {
	for _, abstract := range []bool{false, true} {
		name := "filesystem"
		if abstract {
			name = "abstract"
		}
		t.Run(name, func(t *testing.T) {
			_, original := notifyManager(t, abstract)
			n, err := startNotifications()
			if err != nil {
				t.Fatal(err)
			}
			defer closeNotify(t, n)
			private := notifySocket(n)
			if private == "" || private == original || os.Getenv("NOTIFY_SOCKET") != original {
				t.Fatal("only the child must receive the private notification socket")
			}
			info, err := os.Stat(filepath.Dir(private))
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("private directory permissions: %v, %v", info, err)
			}
			closeNotify(t, n)
			closeNotify(t, n)
			if _, err := os.Stat(filepath.Dir(private)); !os.IsNotExist(err) {
				t.Fatalf("private notification directory remains: %v", err)
			}
			select {
			case err := <-n.failed:
				t.Fatalf("intentional shutdown reported as failure: %v", err)
			default:
			}
		})
	}
}

func TestNotifyMissingSocketIsNoop(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	n, err := startNotifications()
	if err != nil || n != nil {
		t.Fatalf("without a notification socket: %v, %v", n, err)
	}
	closeNotify(t, n)
}

func TestNotifyStartupRefusesInvalidSocket(t *testing.T) {
	for _, address := range []string{"relative", "/nonexistent-berth-notify-test/socket"} {
		t.Run(filepath.Base(address), func(t *testing.T) {
			t.Setenv("NOTIFY_SOCKET", address)
			marker := filepath.Join(t.TempDir(), "started")
			code := Run(Options{Socket: filepath.Join(t.TempDir(), "absent"),
				Argv: []string{"touch", marker}, Stderr: io.Discard})
			if code != 127 {
				t.Fatalf("startup exit = %d, want 127", code)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("child ran despite notification setup failure")
			}
		})
	}
}

func TestNotifyFailedExecCleansSocket(t *testing.T) {
	notifyManager(t, false)
	code := Run(Options{Socket: filepath.Join(t.TempDir(), "absent"),
		Argv: []string{"/nonexistent-berth-notify-test/executable"}, Stderr: io.Discard})
	if code != 127 {
		t.Fatalf("missing executable exit = %d", code)
	}
	entries, err := os.ReadDir(os.Getenv("XDG_RUNTIME_DIR"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed exec left notification state: %v, %v", entries, err)
	}
}

func TestNotifyFailureClosesBarrier(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		name := "target_closed"
		if oversized {
			name = "oversized"
		}
		t.Run(name, func(t *testing.T) {
			manager, _ := notifyManager(t, false)
			n, err := startNotifications()
			if err != nil {
				t.Fatal(err)
			}
			defer closeNotify(t, n)
			if !oversized {
				manager.Close()
			}
			conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: notifySocket(n), Net: "unixgram"})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			data := "BARRIER=1"
			if oversized {
				data = strings.Repeat("X", 4097)
			}
			writeNotify(t, conn, data, int(write.Fd()))
			write.Close()
			select {
			case err := <-n.failed:
				if err == nil {
					t.Fatal("notification transport failure was not surfaced")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("notification transport failure was lost")
			}
			if err := read.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadAll(read); err != nil {
				t.Fatalf("failed forwarding leaked a barrier descriptor: %v", err)
			}
		})
	}
}

func writeNotify(t *testing.T, conn *net.UnixConn, data string, fds ...int) {
	t.Helper()
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var sendErr error
	err = raw.Write(func(fd uintptr) bool {
		_, sendErr = unix.SendmsgN(int(fd), []byte(data), unix.UnixRights(fds...), nil, 0)
		return sendErr != unix.EAGAIN
	})
	if err != nil || sendErr != nil {
		t.Fatalf("send notification: %v, %v", err, sendErr)
	}
}

func TestNotifyExitKeepsPendingError(t *testing.T) {
	failed := make(chan error, 1)
	failed <- io.ErrClosedPipe
	done := make(chan struct{})
	close(done)
	n := &notifications{failed: failed}
	drained := false
	got := <-n.watch(done, func(error) { drained = true })
	if got != io.ErrClosedPipe || drained {
		t.Fatalf("completed child: error=%v, drained=%v; keep the failure without stopping an exited child", got, drained)
	}
}

func TestNotifyFileStore(t *testing.T) {
	manager, _ := notifyManager(t, false)
	n, err := startNotifications()
	if err != nil {
		t.Fatal(err)
	}
	defer closeNotify(t, n)
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: notifySocket(n), Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	state, err := os.CreateTemp(t.TempDir(), "state")
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.WriteString("stored engine state"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	payload := "FDSTORE=1\nFDNAME=engine-state\nX_BERTH_TEST=opaque"
	writeNotify(t, conn, payload, int(state.Fd()))
	if err := manager.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data, control := make([]byte, 4096), make([]byte, 4096)
	count, oobn, _, _, err := manager.ReadMsgUnix(data, control)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := unix.ParseSocketControlMessage(control[:oobn])
	if err != nil {
		t.Fatal(err)
	}
	var received []int
	for _, message := range messages {
		fds, err := unix.ParseUnixRights(&message)
		if err != nil {
			t.Fatal(err)
		}
		received = append(received, fds...)
	}
	if string(data[:count]) != payload || len(received) != 1 {
		for _, fd := range received {
			_ = unix.Close(fd)
		}
		t.Fatalf("file-store notification changed: %q, fds=%v", data[:count], received)
	}
	stored := os.NewFile(uintptr(received[0]), "received-state")
	defer stored.Close()
	content, err := io.ReadAll(stored)
	if err != nil || string(content) != "stored engine state" {
		t.Fatalf("forwarded descriptor: %q, %v", content, err)
	}
}

func TestNotifyChild(t *testing.T) {
	if os.Getenv("BERTH_NOTIFY_CHILD") != "1" {
		return
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: os.Getenv("NOTIFY_SOCKET"), Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("READY=1\nSTATUS=child ready")); err != nil {
		t.Fatal(err)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var sendErr error
	err = raw.Write(func(fd uintptr) bool {
		_, sendErr = unix.SendmsgN(int(fd), []byte("BARRIER=1"), unix.UnixRights(int(write.Fd())), nil, 0)
		return sendErr != unix.EAGAIN
	})
	_ = write.Close()
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(read); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("BERTH_NOTIFY_DENY_CLEANUP") == "1" {
		if err := os.Chmod(filepath.Dir(os.Getenv("NOTIFY_SOCKET")), 0500); err != nil {
			t.Fatal(err)
		}
	}
}
