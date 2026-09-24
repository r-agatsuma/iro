package main

import (
	"os"

	"github.com/r-agatsuma/iro/internal/iro"
)

func main() {
	os.Exit(iro.Execute(os.Args[1:], os.Stdout, os.Stderr, iro.NewService(iro.NewOSCommandRunner(), iro.NewOSFileSystem())))
}
