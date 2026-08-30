package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"

	strategyconfig "github.com/mooyang-code/moox/modules/strategy/internal/config"
)

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCLI(args []string, out, errOut *os.File) error {
	if len(args) == 0 || args[0] != "validate" {
		return errors.New("usage: strategy validate [--space-id <id>] <manifest.yaml>")
	}
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(errOut)
	spaceID := fs.String("space-id", "", "strategy space id")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: strategy validate [--space-id <id>] <manifest.yaml>")
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	manifest, err := strategyconfig.Parse(raw)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if *spaceID == "" {
		_, _ = fmt.Fprintf(out, "valid strategy api=%s kind=%s manifest_hash=%s\n", manifest.APIVersion, manifest.Kind, hex.EncodeToString(sum[:]))
		return nil
	}
	_, _ = fmt.Fprintf(out, "valid strategy api=%s kind=%s space_id=%s manifest_hash=%s\n", manifest.APIVersion, manifest.Kind, *spaceID, hex.EncodeToString(sum[:]))
	return nil
}
