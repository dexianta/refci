package main

import (
	"fmt"
	"os"
	"path/filepath"

	"dexianta/nci/core"
	"dexianta/nci/tui"
)

func main() {
	args := os.Args[1:]

	if len(args) > 0 && args[0] == "init" {
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Error: missing path. Usage: nci init <path>")
			os.Exit(1)
		}
		if err := core.InitRoot(args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		absPath, err := filepath.Abs(args[1])
		if err != nil {
			absPath = args[1]
		}
		fmt.Printf("nci root created at %s\n, run nci to start", absPath)
		return
	}

	if _, err := os.Stat("nci.db"); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "Error: not an nci root. Run 'nci init <path>' to create one.")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}

	sqlite, err := core.OpenDB(core.DBConfig{
		Kind:       core.DBSQLite,
		SQLitePath: filepath.Join(core.Root, "nci.db"),
	})
	if err != nil {
		panic(err.Error())
	}
	repo, err := core.NewSQLiteRepo(sqlite)
	if err != nil {
		panic(err.Error())
	}

	svc := core.NewSvcImpl(repo)
	if err := tui.Run(svc, repo); err != nil {
		fmt.Fprintln(os.Stderr, "tui error:", err)
		os.Exit(1)
	}
}
