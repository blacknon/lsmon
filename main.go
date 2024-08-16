// Copyright (c) 2024 Blacknon. All rights reserved.
// Use of this source code is governed by an MIT license
// that can be found in the LICENSE file.

package main

import (
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	// _ "net/http/pprof"

	"github.com/blacknon/lssh/check"
	"github.com/blacknon/lssh/common"
	"github.com/blacknon/lssh/conf"
	"github.com/blacknon/lssh/list"
	sshcmd "github.com/blacknon/lssh/ssh"
	"golang.org/x/crypto/ssh/terminal"

	mon "github.com/blacknon/lsmon/mon"

	"github.com/urfave/cli"
)

func main() {
	app := LsMon()
	args := common.ParseArgs(app.Flags, os.Args)
	app.Run(args)
}

func LsMon() (app *cli.App) {
	// Default config file path
	defConf := common.GetDefaultConfigPath()

	// Set help templete
	cli.AppHelpTemplate = `NAME:
    {{.Name}} - {{.Usage}}
USAGE:
    {{.HelpName}} {{if .VisibleFlags}}[options]{{end}} [commands...]
    {{if len .Authors}}
AUTHOR:
    {{range .Authors}}{{ . }}{{end}}
    {{end}}{{if .Commands}}
COMMANDS:
    {{range .Commands}}{{if not .HideHelp}}{{join .Names ", "}}{{ "\t"}}{{.Usage}}{{ "\n" }}{{end}}{{end}}{{end}}{{if .VisibleFlags}}
OPTIONS:
    {{range .VisibleFlags}}{{.}}
    {{end}}{{end}}{{if .Copyright }}
COPYRIGHT:
    {{.Copyright}}
    {{end}}{{if .Version}}
VERSION:
    {{.Version}}
    {{end}}
USAGE:
    # connect parallel ssh shell
	lsmon
`

	// Create app
	app = cli.NewApp()
	// app.UseShortOptionHandling = true
	app.Name = "LsMon"
	app.Usage = "TUI list select and parallel ssh client shell."
	app.Copyright = "blacknon(blacknon@orebibou.com)"
	app.Version = "0.1.0"

	// Set options
	app.Flags = []cli.Flag{
		// common option
		cli.StringSliceFlag{Name: "host,H", Usage: "connect `servername`."},
		cli.StringFlag{Name: "file,F", Value: defConf, Usage: "config `filepath`."},
		cli.StringFlag{Name: "logfile,L", Usage: "Set log file path."},

		// Other bool
		cli.BoolFlag{Name: "list,l", Usage: "print server list from config."},
		cli.BoolFlag{Name: "help,h", Usage: "print this help"},
	}
	app.EnableBashCompletion = true
	app.HideHelp = true

	// Run command action
	app.Action = func(c *cli.Context) error {
		// show help messages
		if c.Bool("help") {
			cli.ShowAppHelp(c)
			os.Exit(0)
		}

		logpath := c.String("logfile")
		if logpath == "" {
			logpath = "/dev/null"
		}
		logpath = getAbsPath(logpath)

		logfile, lerr := os.OpenFile(logpath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if lerr != nil {
			log.Fatal(lerr)
		}
		defer logfile.Close()

		log.SetOutput(logfile)

		hosts := c.StringSlice("host")
		confpath := c.String("file")

		// Get config data
		data := conf.Read(confpath)

		// Set `exec command` or `shell` flag
		isMulti := true

		// Extraction server name list from 'data'
		names := conf.GetNameList(data)
		sort.Strings(names)

		// Check list flag
		if c.Bool("list") {
			fmt.Fprintf(os.Stdout, "lssh Server List:\n")
			for v := range names {
				fmt.Fprintf(os.Stdout, "  %s\n", names[v])
			}
			os.Exit(0)
		}

		selected := []string{}
		if len(hosts) > 0 {
			if !check.ExistServer(hosts, names) {
				fmt.Fprintln(os.Stderr, "Input Server not found from list.")
				os.Exit(1)
			} else {
				selected = hosts
			}
		} else {
			// View List And Get Select Line
			l := new(list.ListInfo)
			l.Prompt = "lssh>>"
			l.NameList = names
			l.DataList = data
			l.MultiFlag = isMulti

			l.View()
			selected = l.SelectName
			if selected[0] == "ServerName" {
				fmt.Fprintln(os.Stderr, "Server not selected.")
				os.Exit(1)
			}
		}

		r := new(sshcmd.Run)
		r.ServerList = selected
		r.Conf = data
		r.Conf.Common.ConnectTimeout = 5

		var err error

		// if err
		if err != nil {
			fmt.Printf("Error: %s \n", err)
		}

		// Get stdin data(pipe)
		// TODO(blacknon): os.StdinをReadAllで全部読み込んでから処理する方式だと、ストリームで処理出来ない
		//                 (全部読み込み終わるまで待ってしまう)ので、Reader/Writerによるストリーム処理に切り替える(v0.7.0)
		//                 => flagとして検知させて、あとはpushPipeWriterにos.Stdinを渡すことで対処する
		if runtime.GOOS != "windows" {
			stdin := 0
			if !terminal.IsTerminal(stdin) {
				r.IsStdinPipe = true
			}
		}

		// create AuthMap
		r.CreateAuthMethodMap()

		err = mon.Run(r)
		return err
	}
	return app
}

// getAbsPath return absolute path convert.
// Replace `~` with your home directory.
func getAbsPath(path string) string {
	// Replace home directory
	usr, _ := user.Current()
	path = strings.Replace(path, "~", usr.HomeDir, 1)

	path, _ = filepath.Abs(path)
	return path
}
