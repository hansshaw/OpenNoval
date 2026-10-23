// Command ainovel-desktop is the private backend launched by the desktop shell.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/hansshaw/AInoval-web-ui/internal/entry/desktopui"
)

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err == nil {
		err = desktopui.Run(opts)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "桌面工作台：", err)
		os.Exit(1)
	}
}

func parseOptions(args []string) (desktopui.Options, error) {
	var opts desktopui.Options
	flags := flag.NewFlagSet("ainovel-desktop", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.Listen, "listen", "127.0.0.1:0", "桌面内部监听地址")
	flags.StringVar(&opts.OutputDir, "output", "", "作品目录")
	if err := flags.Parse(args); err != nil {
		return opts, fmt.Errorf("请通过桌面应用启动：%w", err)
	}
	if flags.NArg() != 0 {
		return opts, fmt.Errorf("请通过桌面应用启动，不支持命令行任务")
	}
	return opts, nil
}
