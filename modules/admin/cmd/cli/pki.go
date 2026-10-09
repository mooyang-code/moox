package main

import (
	"errors"
	"flag"
	"io"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
)

func isPKICommand(args []string) bool { return len(args) > 1 && args[1] == "pki" }

func runPKICommand(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 || args[0] != "pki" {
		return errors.New("expected pki subcommand")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	fs := flag.NewFlagSet("pki "+args[1], flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("pki-dir", "", "persistent 0700 control CA directory")
	hostID := fs.String("host-id", "", "canonical host identity")
	address := fs.String("address", "", "bare public IP or DNS host")
	privateAddress := fs.String("private-address", "", "bare private IP or DNS host")
	outputDir := fs.String("output-dir", "", "private output directory; must be new for issue")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *dir == "" {
		return errors.New("pki requires --pki-dir and no positional arguments")
	}
	switch args[1] {
	case "ensure-ca":
		if *hostID != "" || *address != "" || *privateAddress != "" || *outputDir != "" {
			return errors.New("pki ensure-ca accepts only --pki-dir")
		}
	case "issue":
		if *hostID == "" || *address == "" || *outputDir == "" {
			return errors.New("pki issue requires --host-id, --address and --output-dir")
		}
	case "export-ca":
		if *outputDir == "" || *hostID != "" || *address != "" || *privateAddress != "" {
			return errors.New("pki export-ca requires --output-dir")
		}
	default:
		return errors.New("unknown pki subcommand: use ensure-ca, issue, or export-ca")
	}
	store, err := pki.Open(*dir)
	if err != nil {
		return err
	}
	defer store.Close()
	switch args[1] {
	case "ensure-ca":
		info, err := store.EnsureCA()
		if err != nil {
			return err
		}
		return writeJSON(stdout, info)
	case "issue":
		issued, err := store.Issue(pki.HostIdentity{HostID: *hostID, Address: *address, PrivateAddress: *privateAddress}, *outputDir)
		if err != nil {
			return err
		}
		return writeJSON(stdout, issued)
	case "export-ca":
		info, err := store.ExportCA(*outputDir)
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"certificate": info, "ca_file": filepath.Join(*outputDir, "moox-ca.crt")})
	}
	return nil
}
