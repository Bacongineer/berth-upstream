// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package cli

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// taskName is one unit's task in the user's Task Scheduler: berth owns the
// \Berth folder and one logon task per unit in it.
func taskName(name string) string { return `\Berth\` + name }

// unitLogPath is one unit's log file, under the state directory berth
// already keeps its files in: each task appends its output there, since a
// Task Scheduler task has no console to keep it.
func unitLogPath(name string) string {
	return filepath.Join(defaultStateDir(), "logs", name+".log")
}

// taskAction is one unit's command the way the task runs it: cmd executes
// it and appends both of its streams to the unit's log file. The outer
// quote pair is cmd's own: with /c, cmd strips the first and the last
// quote and runs what is left, which is how a path with a space survives.
func taskAction(u Unit) ([]string, error) {
	if len(u.Exec) == 0 {
		return nil, fmt.Errorf("no command for %s", u.Name)
	}
	inner := `"` + u.Exec[0] + `"`
	if args := strings.Join(u.Exec[1:], " "); args != "" {
		inner += " " + args
	}
	inner += ` >> "` + unitLogPath(u.Name) + `" 2>&1`
	return []string{"cmd.exe", "/c", `"` + inner + `"`}, nil
}

// unitTaskXML is one unit's task XML: its action wrapped for the log by
// taskAction.
func unitTaskXML(u Unit, taskUser string) (string, error) {
	action, err := taskAction(u)
	if err != nil {
		return "", err
	}
	wrapped := u
	wrapped.Exec = action
	return renderTaskXML(wrapped, taskUser)
}

// liveUnitDir is where the task XML files wait while schtasks reads them.
func liveUnitDir() (string, error) { return os.TempDir(), nil }

// currentUser is the user the logon tasks run as: the one who installed
// them. Empty when it cannot be read, in which case the tasks name no
// user and Task Scheduler runs them as the installer.
func currentUser() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}

// xmlText is a string safe to put inside the task XML's elements.
func xmlText(s string) string {
	out := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
	return out
}

// renderTaskXML is one unit's Task Scheduler XML: a logon trigger for the
// installing user, an interactive token in their session (where their GPU
// apps are), least privilege, hidden, no execution time limit, and a
// restart on failure of three tries a minute apart. The element names and
// shapes are Microsoft's Task Scheduler schema (the mit/task namespace).
func renderTaskXML(u Unit, taskUser string) (string, error) {
	if len(u.Exec) == 0 {
		return "", fmt.Errorf("no command for %s", u.Name)
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-16"?>` + "\n")
	b.WriteString(`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` + "\n")
	b.WriteString("  <RegistrationInfo>\n")
	b.WriteString("    <Author>berth</Author>\n")
	b.WriteString("    <Description>" + xmlText(u.Description) + "</Description>\n")
	b.WriteString("  </RegistrationInfo>\n")
	b.WriteString("  <Triggers>\n    <LogonTrigger>\n")
	b.WriteString("      <Enabled>true</Enabled>\n")
	if taskUser != "" {
		b.WriteString("      <UserId>" + xmlText(taskUser) + "</UserId>\n")
	}
	b.WriteString("    </LogonTrigger>\n  </Triggers>\n")
	b.WriteString("  <Settings>\n")
	b.WriteString("    <RestartOnFailure>\n      <Interval>PT1M</Interval>\n      <Count>3</Count>\n    </RestartOnFailure>\n")
	b.WriteString("    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>\n")
	b.WriteString("    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>\n")
	b.WriteString("    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>\n")
	b.WriteString("    <StartWhenAvailable>true</StartWhenAvailable>\n")
	b.WriteString("    <Hidden>true</Hidden>\n")
	b.WriteString("    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>\n")
	b.WriteString("  </Settings>\n")
	b.WriteString("  <Principals>\n    <Principal>\n")
	if taskUser != "" {
		b.WriteString("      <UserId>" + xmlText(taskUser) + "</UserId>\n")
	}
	b.WriteString("      <LogonType>InteractiveToken</LogonType>\n")
	b.WriteString("      <RunLevel>LeastPrivilege</RunLevel>\n")
	b.WriteString("    </Principal>\n  </Principals>\n")
	b.WriteString("  <Actions>\n    <Exec>\n")
	b.WriteString("      <Command>" + xmlText(u.Exec[0]) + "</Command>\n")
	b.WriteString("      <Arguments>" + xmlText(strings.Join(u.Exec[1:], " ")) + "</Arguments>\n")
	b.WriteString("    </Exec>\n  </Actions>\n")
	b.WriteString("</Task>\n")
	return b.String(), nil
}

// utf16le is the XML file's bytes the way schtasks writes and reads its
// own exports: a BOM, then little-endian UTF-16.
func utf16le(s string) []byte {
	out := []byte{0xff, 0xfe}
	for _, r := range s {
		if r > 0xffff {
			r -= 0x10000
			halfs := []uint32{uint32(0xd800 + (r >> 10)), uint32(0xdc00 + (r & 0x3ff))}
			for _, half := range halfs {
				out = append(out, byte(half), byte(half>>8))
			}
			continue
		}
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

// serviceInstall creates one logon task per unit with schtasks, then starts
// each task. Each XML file is written next to the temp directory, handed to
// schtasks, and deleted again. A task already there whose XML differs from
// what berth would write is kept in the state directory as .bak, like the
// systemd path keeps a differing unit file beside the new one. With dryRun
// nothing is created or started.
func serviceInstall(ctx context.Context, ops serviceOps, unitsList []Unit, dryRun bool) (*serviceDoc, *Error) {
	doc := &serviceDoc{DryRun: dryRun, Units: make([]serviceUnit, 0, len(unitsList)), Commands: []string{}}
	taskUser := currentUser()
	rows := map[string]*serviceUnit{}
	createOne := func(u Unit) (*serviceUnit, *Error) {
		task := taskName(u.Name)
		row := serviceUnit{Unit: u.Name, Target: task}
		xml, err := unitTaskXML(u, taskUser)
		if err != nil {
			return nil, Systemf("%s: %v", u.Name, err)
		}
		row.Content = xml
		if dryRun {
			row.Action = "would be created and started"
			return &row, nil
		}
		// The previous task, when it differs, is kept as .bak before /F
		// replaces it.
		out, err := ops.run(ctx, "schtasks.exe", "/Query", "/TN", task, "/XML")
		if err != nil && !taskNotFound(out) {
			return nil, Systemf("schtasks /Query /TN %s /XML failed:\n%s", task, strings.TrimSpace(out))
		}
		if err == nil && strings.TrimSpace(out) != strings.TrimSpace(xml) {
			bak := filepath.Join(defaultStateDir(), u.Name+".xml.bak")
			if err := os.MkdirAll(filepath.Dir(bak), 0o755); err != nil {
				return nil, Systemf("keep the previous copy of %s: %v", task, err)
			}
			if err := os.WriteFile(bak, []byte(out), 0o644); err != nil {
				return nil, Systemf("keep the previous copy of %s: %v", task, err)
			}
			row.BackedUp = bak
		}
		dir, err := ops.unitDir()
		if err != nil {
			return nil, Systemf("find the temp directory: %v", err)
		}
		f, err := os.CreateTemp(dir, "berth-"+u.Name+"-*.xml")
		if err != nil {
			return nil, Systemf("write the task XML for %s: %v", u.Name, err)
		}
		path := f.Name()
		if _, err := f.Write(utf16le(xml)); err != nil {
			f.Close()
			os.Remove(path)
			return nil, Systemf("write the task XML for %s: %v", u.Name, err)
		}
		if err := f.Close(); err != nil {
			os.Remove(path)
			return nil, Systemf("write the task XML for %s: %v", u.Name, err)
		}
		cmd := "schtasks /Create /TN " + task + " /XML " + path + " /F"
		if out, err := ops.run(ctx, "schtasks.exe", "/Create", "/TN", task, "/XML", path, "/F"); err != nil {
			os.Remove(path)
			return nil, Systemf("%s failed:\n%s", cmd, strings.TrimSpace(out))
		}
		os.Remove(path)
		doc.Commands = append(doc.Commands, cmd)
		// The log file the task appends to must exist as a directory
		// before its first start: the redirection creates the file, not
		// the path to it.
		if err := os.MkdirAll(filepath.Dir(unitLogPath(u.Name)), 0o755); err != nil {
			return nil, Systemf("create the log directory for %s: %v", u.Name, err)
		}
		row.Action = "created"
		return &row, nil
	}
	for _, u := range unitsList {
		row, e := createOne(u)
		if e != nil {
			return nil, e
		}
		rows[row.Unit] = row
	}
	for _, u := range unitsList {
		row := rows[u.Name]
		if dryRun {
			doc.Units = append(doc.Units, *row)
			continue
		}
		task := taskName(u.Name)
		cmd := "schtasks /Run /TN " + task
		out, err := ops.run(ctx, "schtasks.exe", "/Run", "/TN", task)
		if err != nil {
			// The task is in place and starts at the next logon; the
			// failed start is reported, not fatal.
			row.Action = "created, not started (" + strings.TrimSpace(out) + ")"
		} else {
			doc.Commands = append(doc.Commands, cmd)
			row.Action = "created, started"
		}
	}
	if !dryRun {
		for _, u := range unitsList {
			doc.Units = append(doc.Units, *rows[u.Name])
		}
	}
	return doc, nil
}

// serviceUninstall deletes the tasks schtasks can find and reports the
// rest as never installed. With dryRun it asks schtasks which tasks exist,
// which is a read, and changes nothing.
func serviceUninstall(ctx context.Context, ops serviceOps, unitsList []Unit, dryRun bool) (*serviceDoc, *Error) {
	doc := &serviceDoc{DryRun: dryRun, Units: make([]serviceUnit, 0, len(unitsList)), Commands: []string{}}
	for _, u := range unitsList {
		task := taskName(u.Name)
		row := serviceUnit{Unit: u.Name, Target: task}
		if dryRun {
			out, err := ops.run(ctx, "schtasks.exe", "/Query", "/TN", task, "/FO", "CSV", "/NH")
			if err != nil && !taskNotFound(out) {
				return nil, Systemf("schtasks /Query /TN %s failed:\n%s", task, strings.TrimSpace(out))
			}
			if err != nil {
				row.Action = "not installed"
			} else {
				row.Action = "would be deleted"
			}
			doc.Units = append(doc.Units, row)
			continue
		}
		cmd := "schtasks /Delete /TN " + task + " /F"
		out, err := ops.run(ctx, "schtasks.exe", "/Delete", "/TN", task, "/F")
		if err != nil {
			if taskNotFound(out) {
				row.Action = "not installed"
			} else {
				return nil, Systemf("%s failed:\n%s", cmd, strings.TrimSpace(out))
			}
		} else {
			doc.Commands = append(doc.Commands, cmd)
			row.Action = "deleted"
		}
		doc.Units = append(doc.Units, row)
	}
	return doc, nil
}

// serviceStatus asks schtasks for each task. A task schtasks cannot find
// is not installed; any other failure of the query is a system failure. An
// installed task names the log file its output appends to.
func serviceStatus(ctx context.Context, ops serviceOps, unitsList []Unit) (*serviceDoc, *Error) {
	doc := &serviceDoc{Units: make([]serviceUnit, 0, len(unitsList)), Commands: []string{}}
	for _, u := range unitsList {
		task := taskName(u.Name)
		row := serviceUnit{Unit: u.Name, Target: task}
		out, err := ops.run(ctx, "schtasks.exe", "/Query", "/TN", task, "/FO", "CSV", "/NH")
		if err != nil {
			if !taskNotFound(out) {
				return nil, Systemf("schtasks /Query /TN %s failed:\n%s", task, strings.TrimSpace(out))
			}
			doc.Units = append(doc.Units, row)
			continue
		}
		row.Installed = true
		row.Log = unitLogPath(u.Name)
		if fields, err := csvFields(out); err == nil && len(fields) >= 2 {
			row.Running = fields[1] == "Running"
		}
		doc.Units = append(doc.Units, row)
	}
	return doc, nil
}

// taskNotFound is schtasks saying the task does not exist: it fails the
// query and names the task as not found, which the commands read as
// "not installed", not as a broken machine.
func taskNotFound(out string) bool {
	return strings.Contains(strings.ToLower(out), "cannot find") ||
		strings.Contains(strings.ToLower(out), "not found") ||
		strings.Contains(strings.ToLower(out), "does not exist")
}

// csvFields splits one schtasks CSV line into its fields.
func csvFields(line string) ([]string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, fmt.Errorf("empty line")
	}
	var fields []string
	var cur strings.Builder
	inQuotes := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case ch == '"':
			if inQuotes && i+1 < len(line) && line[i+1] == '"' {
				cur.WriteByte('"')
				i++
			} else {
				inQuotes = !inQuotes
			}
		case ch == ',' && !inQuotes:
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(ch)
		}
	}
	fields = append(fields, cur.String())
	return fields, nil
}
