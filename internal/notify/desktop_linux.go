// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package notify

import (
	"fmt"
	"os/exec"
)

// Desktop is the desktop's notification service: notify-send, which
// speaks the freedesktop notification interface every Linux desktop and
// notification daemon (mako, dunst, swaync, KDE, GNOME) serves.
func Desktop() (Sender, error) {
	bin, err := exec.LookPath("notify-send")
	if err != nil {
		return nil, fmt.Errorf("notify-send is not installed (libnotify): %w", err)
	}
	return notifySend(bin), nil
}

type notifySend string

func (bin notifySend) Send(n Note) error {
	urgency := "normal"
	if n.Level == Error {
		urgency = "critical"
	}
	return exec.Command(string(bin), "-a", "berth", "-u", urgency, n.Title, n.Body).Run()
}
