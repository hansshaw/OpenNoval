package main

import "testing"

func TestWebCLIOptions(t *testing.T) {
	opts, _, err := parseCLIOptions([]string{"--web", "--open-browser", "--listen", "127.0.0.1:9090", "--output", "my-book"})
	if err != nil || !opts.Web || !opts.OpenBrowser || opts.Listen != "127.0.0.1:9090" || opts.OutputDir != "my-book" {
		t.Fatalf("%+v %v", opts, err)
	}
	for _, args := range [][]string{{"--web", "--headless"}, {"--web", "--version"}, {"--listen", "127.0.0.1:8080"}, {"--web", "--prompt", "hello"}, {"--web", "--listen"}, {"--output", "x"}, {"--web", "update"}} {
		if _, _, err := parseCLIOptions(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, _, err := parseCLIOptions([]string{"--open-browser"}); err == nil {
		t.Fatal("accepted --open-browser without --web")
	}
}
