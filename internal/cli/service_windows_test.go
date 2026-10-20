// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package cli

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRenderTaskXML(t *testing.T) {
	u := Unit{
		Name:        "berthd",
		Exec:        []string{`C:\berth\berth.exe`, "daemon", "--gpu", "0", "--poll", "2s"},
		Description: "berth - GPU lease broker (one card, many inference engines)",
	}
	doc, err := renderTaskXML(u, `ACME\trevin`)
	if err != nil {
		t.Fatal(err)
	}
	// The token walk is the well-formedness check.
	// The rendering is UTF-8 in memory; only the file schtasks reads is
	// UTF-16, so the declaration's charset is read through as is.
	dec := xml.NewDecoder(strings.NewReader(doc))
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	for {
		if _, err := dec.Token(); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("the task XML is not well-formed: %v", err)
		}
	}
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-16"?>`,
		`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">`,
		`<LogonTrigger>`,
		`<Enabled>true</Enabled>`,
		`<UserId>ACME\trevin</UserId>`,
		`<LogonType>InteractiveToken</LogonType>`,
		`<RunLevel>LeastPrivilege</RunLevel>`,
		`<StartWhenAvailable>true</StartWhenAvailable>`,
		`<Hidden>true</Hidden>`,
		`<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>`,
		`<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>`,
		`<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>`,
		`<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>`,
		`<RestartOnFailure>`,
		`<Interval>PT1M</Interval>`,
		`<Count>3</Count>`,
		`<Command>C:\berth\berth.exe</Command>`,
		`<Arguments>daemon --gpu 0 --poll 2s</Arguments>`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the task XML lacks %s:\n%s", want, doc)
		}
	}
	if strings.Count(doc, `<UserId>`) != 2 {
		t.Errorf("the user is named in the trigger and the principal:\n%s", doc)
	}

	escaped, err := renderTaskXML(Unit{Name: "x", Exec: []string{"c"}, Description: "a & b <c>"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(escaped, "<Description>a &amp; b &lt;c&gt;</Description>") {
		t.Errorf("the description is XML-escaped:\n%s", escaped)
	}
	if strings.Contains(escaped, "<UserId>") {
		t.Errorf("no user is named when it is unknown:\n%s", escaped)
	}
}

func TestUtf16le(t *testing.T) {
	cases := map[string]string{
		"AB":    "AB",
		"héllo": "héllo",
	}
	for in, want := range cases {
		dec := utf16decode(utf16le(in))
		if dec != want {
			t.Errorf("utf16le round trip of %q = %q", in, dec)
		}
	}
	// A supplementary-plane character is a surrogate pair in UTF-16.
	if got := utf16decode(utf16le("旗\U0001F680")); got != "旗\U0001F680" {
		t.Errorf("the surrogate pair round-trips, got %q", got)
	}
	b := utf16le("A")
	if len(b) != 4 || b[0] != 0xff || b[1] != 0xfe || b[2] != 'A' || b[3] != 0 {
		t.Errorf("the BOM and little-endian form, got % x", b)
	}
}

func utf16decode(b []byte) string {
	if len(b) < 2 || b[0] != 0xff || b[1] != 0xfe {
		return ""
	}
	var s strings.Builder
	for i := 2; i+1 < len(b); i += 2 {
		r := rune(b[i]) | rune(b[i+1])<<8
		if r >= 0xd800 && r <= 0xdbff && i+3 < len(b) {
			r2 := rune(b[i+2]) | rune(b[i+3])<<8
			s.WriteRune(0x10000 + (r-0xd800)<<10 + (r2 - 0xdc00))
			i += 2
		} else {
			s.WriteRune(r)
		}
	}
	return s.String()
}

func TestCsvFields(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{`"\Berth\berthd","Running","12:00:00"`, []string{`\Berth\berthd`, "Running", "12:00:00"}},
		{`"a ""quoted"" b",Ready`, []string{`a "quoted" b`, "Ready"}},
		{`plain,Running`, []string{"plain", "Running"}},
	}
	for _, c := range cases {
		got, err := csvFields(c.line)
		if err != nil || len(got) != len(c.want) {
			t.Errorf("csvFields(%q) = %v, %v; want %v", c.line, got, err, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("csvFields(%q) = %v; want %v", c.line, got, c.want)
			}
		}
	}
}

func TestTaskNotFound(t *testing.T) {
	for _, out := range []string{
		`ERROR: The system cannot find the file specified.`,
		`ERROR: The task is not found.`,
		`The task does not exist`,
	} {
		if !taskNotFound(out) {
			t.Errorf("taskNotFound(%q) = false", out)
		}
	}
	if taskNotFound(`ERROR: Access is denied.`) {
		t.Error("an access failure is not a missing task")
	}
}

func TestServiceInstallWindows(t *testing.T) {
	dir := t.TempDir()
	const exe = `C:\berth\berth.exe`
	// Each /Create's XML is read while schtasks would read it: install
	// removes the file once the task exists.
	var xmlFiles []string
	var xmlTexts []string
	ops, calls := fakeOps(t, dir, func(a argv) (string, error) {
		if a.name == "schtasks.exe" && a.args[0] == "/Create" {
			b, err := os.ReadFile(a.args[4])
			if err != nil {
				t.Errorf("the XML file schtasks was given: %v", err)
			}
			xmlFiles = append(xmlFiles, a.args[4])
			xmlTexts = append(xmlTexts, utf16decode(b))
		}
		return "", nil
	})

	doc, e := serviceInstall(context.Background(), ops, serviceUnits(exe, true, false), false)
	if e != nil {
		t.Fatalf("install failed: %v", e)
	}
	if len(*calls) != 2 {
		t.Fatalf("one schtasks call per unit, got %v", *calls)
	}
	if len(xmlFiles) != 2 {
		t.Fatalf("one XML per task, got %v", xmlFiles)
	}
	if (*calls)[0].String() != "schtasks.exe /Create /TN \\Berth\\berthd /XML "+xmlFiles[0]+" /F" {
		t.Errorf("the create command, got %s", (*calls)[0])
	}
	text := xmlTexts[0]
	if !strings.Contains(text, `<Command>C:\berth\berth.exe</Command>`) || !strings.Contains(text, `<Arguments>daemon --gpu 0 --poll 2s</Arguments>`) {
		t.Errorf("the XML runs the user's binary:\n%s", text)
	}
	if strings.Contains(text, "berth-gateway") {
		t.Errorf("each task names its own unit:\n%s", text)
	}
	if _, err := os.Stat(xmlFiles[0]); !os.IsNotExist(err) {
		t.Errorf("the XML file is deleted after schtasks read it: %v", err)
	}
	if doc.Units[0].Action != "created" {
		t.Errorf("the task is created, got %+v", doc.Units[0])
	}
}

func TestServiceInstallWindowsCommandFails(t *testing.T) {
	dir := t.TempDir()
	ops, _ := fakeOps(t, dir, func(a argv) (string, error) {
		return "ERROR: Access is denied.", errors.New("exit status 1")
	})

	_, e := serviceInstall(context.Background(), ops, serviceUnits(`C:\berth\berth.exe`, false, false), false)
	if e == nil || e.Code != ExitSystem {
		t.Errorf("a failing schtasks is a system failure, got %+v", e)
	}
	if !strings.Contains(e.Msg, "Access is denied") {
		t.Errorf("the failure carries the command's output, got %q", e.Msg)
	}
}

func TestServiceStatusWindows(t *testing.T) {
	ops, _ := fakeOps(t, t.TempDir(), func(a argv) (string, error) {
		if a.args[0] == "/Query" {
			switch a.args[2] {
			case `\Berth\berthd`:
				return `"\Berth\berthd","Running","N/A","N/A","N/A","N/A"`, nil
			case `\Berth\berth-gateway`:
				return `ERROR: The system cannot find the file specified.`, errors.New("exit status 1")
			case `\Berth\berth-harbor`:
				return `"\Berth\berth-harbor","Ready","N/A","N/A","N/A","N/A"`, nil
			}
		}
		return "", errors.New("unexpected call " + a.String())
	})

	doc, e := serviceStatus(context.Background(), ops, allUnits())
	if e != nil {
		t.Fatalf("status failed: %v", e)
	}
	byName := map[string]serviceUnit{}
	for _, u := range doc.Units {
		byName[u.Unit] = u
	}
	if !byName["berthd"].Installed || !byName["berthd"].Running {
		t.Errorf("berthd is installed and running, got %+v", byName["berthd"])
	}
	if byName["berth-gateway"].Installed {
		t.Errorf("a task schtasks cannot find is not installed, got %+v", byName["berth-gateway"])
	}
	if !byName["berth-harbor"].Installed || byName["berth-harbor"].Running {
		t.Errorf("berth-harbor is installed and ready, not running, got %+v", byName["berth-harbor"])
	}
}

func TestServiceUninstallWindows(t *testing.T) {
	ops, calls := fakeOps(t, t.TempDir(), func(a argv) (string, error) {
		if a.args[0] == "/Delete" {
			if a.args[2] == `\Berth\berth-gateway` {
				return `ERROR: The system cannot find the file specified.`, errors.New("exit status 1")
			}
		}
		return "", nil
	})

	doc, e := serviceUninstall(context.Background(), ops, allUnits(), false)
	if e != nil {
		t.Fatalf("uninstall failed: %v", e)
	}
	byName := map[string]serviceUnit{}
	for _, u := range doc.Units {
		byName[u.Unit] = u
	}
	if byName["berthd"].Action != "deleted" || byName["berth-harbor"].Action != "deleted" {
		t.Errorf("found tasks are deleted, got %+v", doc.Units)
	}
	if byName["berth-gateway"].Action != "not installed" {
		t.Errorf("a missing task is reported, not an error, got %+v", byName["berth-gateway"])
	}
	if len(*calls) != 3 {
		t.Errorf("one delete per unit, got %v", *calls)
	}
}
