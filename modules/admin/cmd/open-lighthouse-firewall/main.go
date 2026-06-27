package main

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/common/crypto"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: open-lighthouse-firewall <moox.db> <public-ip> [ports]\n")
		os.Exit(2)
	}
	dbPath := os.Args[1]
	publicIP := os.Args[2]

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fatal(err)
	}
	defer db.Close()

	var encSecretID, encSecretKey string
	err = db.QueryRow(`SELECT c_secret_id, c_secret_key FROM t_cloud_accounts WHERE c_invalid = 0 ORDER BY c_id LIMIT 1`).Scan(&encSecretID, &encSecretKey)
	if err != nil {
		fatal(err)
	}

	key := crypto.GetEncryptionKey()
	secretID, err := crypto.AESDecrypt(encSecretID, key)
	if err != nil {
		fatal(err)
	}
	secretKey, err := crypto.AESDecrypt(encSecretKey, key)
	if err != nil {
		fatal(err)
	}

	cliPath := os.Getenv("MOOX_CLI")
	if cliPath == "" {
		cliPath = "moox-cli"
	}

	ports := "11000,10080,20200,20201,20202"
	if len(os.Args) >= 4 && strings.TrimSpace(os.Args[3]) != "" {
		ports = strings.TrimSpace(os.Args[3])
	}

	cmd := exec.Command(cliPath, "ops", "tencent", "lighthouse", "firewall", "add",
		"--public-ip", publicIP,
		"--ports", ports,
		"--description", "moox services",
	)
	cmd.Env = append(os.Environ(),
		"TENCENTCLOUD_SECRET_ID="+secretID,
		"TENCENTCLOUD_SECRET_KEY="+secretKey,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
