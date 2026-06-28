package main

import (
	"log"
	"os"
	"os/exec"

	"router-eval/internal/cli"
)

func main() {
	if err := run(os.Args); err != nil {
		log.Fatal(err)
	}
}

var execLookPath = exec.LookPath

func run(args []string) error {
	return cli.Execute(args, cli.Options{ExecLookPath: execLookPath})
}
