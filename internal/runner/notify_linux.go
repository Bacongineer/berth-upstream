// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

func startNotifications() (*notifications, error) {
	original := os.Getenv("NOTIFY_SOCKET")
	if original == "" {
		return nil, nil
	}
	if original[0] != '/' && original[0] != '@' {
		return nil, errors.New("unsupported NOTIFY_SOCKET address")
	}
	target, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: original, Net: "unixgram"})
	if err != nil {
		return nil, fmt.Errorf("connect notification socket: %w", err)
	}
	dir, err := os.MkdirTemp(os.Getenv("XDG_RUNTIME_DIR"), "berth-notify-")
	if err != nil {
		target.Close()
		return nil, err
	}
	path := filepath.Join(dir, "notify")
	source, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		target.Close()
		return nil, errors.Join(err, os.RemoveAll(dir))
	}
	failed := make(chan error, 1)
	finished := make(chan struct{})
	var closing atomic.Bool
	go func() {
		defer close(finished)
		defer source.Close()
		for {
			if err := forwardNotification(source, target); err != nil {
				if !closing.Load() {
					failed <- err
				}
				return
			}
		}
	}()
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "NOTIFY_SOCKET=") {
			env = append(env, entry)
		}
	}
	env = append(env, "NOTIFY_SOCKET="+path)
	return &notifications{env: env, failed: failed, stop: sync.OnceValue(func() error {
		closing.Store(true)
		source.Close()
		target.Close()
		<-finished
		return os.RemoveAll(dir)
	})}, nil
}

func forwardNotification(source, target *net.UnixConn) error {
	// systemd's receiver uses NOTIFY_BUFFER_MAX=PIPE_BUF and NOTIFY_FD_MAX=768.
	data, control := make([]byte, 4096), make([]byte, unix.CmsgSpace(768*4))
	raw, err := source.SyscallConn()
	if err != nil {
		return err
	}
	var n, oobn, flags int
	var receiveErr error
	err = raw.Read(func(fd uintptr) bool {
		for {
			n, oobn, flags, _, receiveErr = unix.Recvmsg(int(fd), data, control, unix.MSG_CMSG_CLOEXEC)
			if receiveErr != unix.EINTR {
				return receiveErr != unix.EAGAIN
			}
		}
	})
	if err != nil {
		return err
	}
	if receiveErr != nil {
		return receiveErr
	}
	messages, err := unix.ParseSocketControlMessage(control[:oobn])
	if err != nil {
		return err
	}
	var fds []int
	defer func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}()
	for _, message := range messages {
		if message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SCM_RIGHTS {
			rights, err := unix.ParseUnixRights(&message)
			if err != nil {
				return err
			}
			fds = append(fds, rights...)
		}
	}
	if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		return errors.New("truncated notification datagram")
	}
	var ancillary []byte
	if len(fds) > 0 {
		ancillary = unix.UnixRights(fds...)
	}
	raw, err = target.SyscallConn()
	if err != nil {
		return err
	}
	var sendErr error
	err = raw.Write(func(fd uintptr) bool {
		for {
			_, sendErr = unix.SendmsgN(int(fd), data[:n], ancillary, nil, 0)
			if sendErr != unix.EINTR {
				return sendErr != unix.EAGAIN
			}
		}
	})
	if err != nil {
		return err
	}
	return sendErr
}
