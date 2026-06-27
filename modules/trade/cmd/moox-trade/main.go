package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
)

func main() {
	payload, err := json.Marshal(service.New("trade").Health())
	if err != nil {
		fmt.Fprintf(os.Stderr, "{\"error\":\"marshal_failed\",\"message\":%q}\n", err.Error())
		os.Exit(1)
	}
	fmt.Println(string(payload))
}
