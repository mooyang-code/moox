package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/cli/internal/controlclient"
)

func main() {
	base := os.Getenv("CTL_URL")
	if base == "" {
		base = "http://106.53.107.122:18080"
	}
	c := controlclient.New(base)
	c.ServiceAuth = &controlclient.ServiceAuthConfig{
		Version:    "moox-auth-v1",
		AccessKey:  "moox-service",
		SecretKey:  "moox-service-secret-change-me",
		ExpireSecs: 1800,
	}
	job, err := c.QueryJob(context.Background(), "probe-nonexistent-job-id")
	if err != nil {
		fmt.Println("ERR:", err)
		return
	}
	fmt.Printf("OK job=%+v\n", job)
}
