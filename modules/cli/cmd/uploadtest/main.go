package main

import (
	"bytes"
	"context"
	"fmt"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"io/fs"
	"os"
	"time"
)

func main() {
	wd, _ := os.Getwd()
	s, e := setupconfig.Load("./moox.toml", wd)
	if e != nil {
		panic(e)
	}
	h := s.Manifest.ControlHost
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, e := setupssh.Dial(ctx, setupssh.Target{Name: h.Name, Address: h.Address, Port: h.Port, Username: h.Username}, h.Password, setupssh.Options{Timeout: 20 * time.Second})
	if e != nil {
		panic(e)
	}
	defer c.Close()
	b := []byte("ok\n")
	e = c.Upload(ctx, bytes.NewReader(b), int64(len(b)), "/tmp/moox-upload-test", fs.FileMode(0644))
	if e != nil {
		panic(e)
	}
	r, e := c.Run(ctx, []string{"sh", "-lc", "cat /tmp/moox-upload-test; rm -f /tmp/moox-upload-test"}, nil)
	if e != nil {
		panic(e)
	}
	fmt.Print(r.Stdout)
}
