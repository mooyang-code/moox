package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: remotefile [--host control|storage] [--get] <local-file> <remote-path>")
		os.Exit(2)
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	snapshot, err := setupconfig.Load("./moox.toml", wd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	args := os.Args[1:]
	host := snapshot.Manifest.ControlHost
	get := false
	for len(args) >= 2 {
		if args[0] == "--host" {
			switch args[1] {
			case "storage":
				host = snapshot.Manifest.StorageHost
			case "control":
				host = snapshot.Manifest.ControlHost
			default:
				fmt.Fprintf(os.Stderr, "unknown host %q\n", args[1])
				os.Exit(2)
			}
			args = args[2:]
			continue
		}
		if args[0] == "--get" {
			get = true
			args = args[1:]
			continue
		}
		break
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: remotefile [--host control|storage] [--get] <local-file> <remote-path>")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	client, err := setupssh.Dial(ctx, setupssh.Target{
		Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username,
	}, host.Password, setupssh.Options{Timeout: 20 * time.Second})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer client.Close()
	if get {
		downloader, ok := client.(interface {
			Download(context.Context, string, io.Writer) (int64, error)
		})
		if !ok {
			fmt.Fprintln(os.Stderr, "ssh download not supported")
			os.Exit(1)
		}
		file, err := os.Create(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer file.Close()
		n, err := downloader.Download(ctx, args[1], file)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := file.Chmod(0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("downloaded %s@%s -> %s (%d bytes)\n", host.Name, args[1], args[0], n)
		return
	}
	file, err := os.Open(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := client.Upload(ctx, io.LimitReader(file, info.Size()), info.Size(), args[1], fs.FileMode(0o755)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("uploaded %s -> %s@%s (%d bytes)\n", args[0], host.Name, args[1], info.Size())
}
