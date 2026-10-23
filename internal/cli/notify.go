// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/notify"
	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/server"
	"github.com/T-Crypt/berth/internal/suite"
)

func runNotify(c *Context) int {
	fs := c.FlagSet()
	sock := fs.String("socket", defaultSocket(), "the node's berthd socket ($BERTH_SOCKET)")
	cfgPath := fs.String("config", config.DefaultPath(), "berth.toml: [notify] enabled and level ($BERTH_CONFIG)")
	levelFlag := fs.String("level", "", "error or info, over [notify] level in berth.toml, itself error")
	if code, done := c.Parse(fs); done {
		return code
	}
	if code, done := c.noArgs(); done {
		return code
	}
	cfg, _, err := suite.Merged(*cfgPath)
	if err != nil {
		return c.fail(Failf("config invalid: %s", *cfgPath).Detail("%v", err))
	}
	if cfg.Notify.Enabled != nil && !*cfg.Notify.Enabled {
		c.Printf("notifications are off ([notify] enabled = false in %s)\n", *cfgPath)
		return ExitOK
	}
	raw := cfg.Notify.Level
	if *levelFlag != "" {
		raw = *levelFlag
	}
	level, err := notify.ParseLevel(raw)
	if err != nil {
		return c.fail(Failf("%v", err))
	}
	sender, err := notify.Desktop()
	if err != nil {
		return c.fail(Systemf("%v", err))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := c.Log()
	log.Info("notifications on", "level", raw, "socket", *sock)
	notify.Follow(ctx, eventSource(*sock), sender, level, time.Now(), 2*time.Second, log)
	return ExitOK
}

// eventSource follows berthd's events on sock: the backlog, then each new
// event, until berthd closes the stream or ctx ends.
func eventSource(sock string) notify.Source {
	return func(ctx context.Context) (<-chan proto.Event, error) {
		conn, raw, err := server.Dial(sock)
		if err != nil {
			return nil, err
		}
		if err := conn.Send(proto.Msg{Op: proto.OpEvents, Watch: true}); err != nil {
			raw.Close()
			return nil, err
		}
		out := make(chan proto.Event)
		go func() { <-ctx.Done(); raw.Close() }()
		go func() {
			defer close(out)
			defer raw.Close()
			for {
				m, err := conn.Recv()
				if err != nil {
					return
				}
				if m.Event != nil {
					select {
					case out <- *m.Event:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
		return out, nil
	}
}
