package peer

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

const PeerTokenHeader = "X-MooX-Monitor-Peer-Token"

type CheckSnapshot struct {
	CheckID string `json:"check_id"`
	Status  string `json:"status"`
}

type AlertEventSnapshot struct {
	EventID   string `json:"event_id"`
	EventType string `json:"event_type"`
	CreatedAt string `json:"created_at"`
}

type Snapshot struct {
	InstanceID  string               `json:"instance_id"`
	BaseURL     string               `json:"base_url"`
	ObservedAt  time.Time            `json:"observed_at"`
	Checks      []CheckSnapshot      `json:"checks"`
	AlertEvents []AlertEventSnapshot `json:"alert_events"`
}

type HTTPOptions struct {
	Token    string
	Health   http.Handler
	Snapshot func(context.Context) Snapshot
}

func NewHTTPHandler(opts HTTPOptions) http.Handler {
	mux := http.NewServeMux()
	if opts.Health != nil {
		mux.Handle("/healthz", opts.Health)
	}
	mux.HandleFunc("/internal/monitor/v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if opts.Token != "" && r.Header.Get(PeerTokenHeader) != opts.Token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		snapshot := Snapshot{ObservedAt: time.Now().UTC()}
		if opts.Snapshot != nil {
			snapshot = opts.Snapshot(r.Context())
			if snapshot.ObservedAt.IsZero() {
				snapshot.ObservedAt = time.Now().UTC()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot)
	})
	return mux
}
