package main

import "testing"

func TestDesktopOptions(t *testing.T) {
	opts, err := parseOptions([]string{"--listen", "127.0.0.1:0", "--output", "my-book"})
	if err != nil || opts.Listen != "127.0.0.1:0" || opts.OutputDir != "my-book" {
		t.Fatalf("opts=%+v err=%v", opts, err)
	}
	for _, args := range [][]string{{"--web"}, {"--open-browser"}, {"--headless"}, {"--prompt", "hello"}, {"--listen"}, {"update"}, {"eval"}, {"--output", "x", "task"}} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("accepted obsolete public entry %v", args)
		}
	}
}
