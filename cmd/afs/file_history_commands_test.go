package main

import (
	"strings"
	"testing"
)

func TestFileHistoryCompatibilityHelpAndValidationStayOffline(t *testing.T) {
	for _, command := range [][]string{{"history", "list", "--help"}, {"history", "restore", "--help"}, {"history", "serve", "--help"}} {
		out, err := captureStdout(t, func() error {
			return runCLI(append([]string{"--config", "/missing", "--redis", "invalid"}, command...))
		})
		if err != nil || !strings.Contains(out, "Usage: afs") {
			t.Fatalf("%v: %q %v", command, out, err)
		}
	}
	a := &app{}
	for _, args := range [][]string{
		{}, {"unknown"}, {"list", "ws", "file", "--order", "wrong"}, {"list", "ws", "file", "--limit", "1001"},
		{"show", "ws", "file"}, {"restore", "ws", "file", "--file-id", "x"}, {"restore", "ws", "file", "--version", "x", "--ordinal", "1"},
		{"diff", "ws", "file"}, {"diff", "ws", "file", "--from-ref", "head", "--from-version", "x"}, {"undelete", "ws", "../escape"},
	} {
		if err := a.historyCommand(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, args := range [][]string{{"extra"}, {"--listen", "0.0.0.0:8091"}, {"--listen", ":8091"}, {"--allow-origin", "http://localhost:5173/path"}} {
		if err := a.serveHistoryCommand(args); err == nil {
			t.Fatalf("accepted serve %v", args)
		}
	}
	if a.rdb != nil {
		t.Fatal("validation accessed Redis")
	}
}
