package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/nats-io/nats.go"
	"gopkg.in/yaml.v3"
)

type credentialFile struct {
	Username             string   `yaml:"username"`
	Password             string   `yaml:"password"`
	Token                string   `yaml:"token"`
	EventBusToken        string   `yaml:"eventbus_token"`
	MonitorEventBusToken string   `yaml:"monitor_eventbus_token"`
	URLs                 []string `yaml:"urls"`
}

func main() {
	if len(os.Args) < 4 || len(os.Args) > 5 {
		panic("usage: inspect URL credential ca [stream-seq]")
	}
	credentialRaw, err := os.ReadFile(os.Args[2])
	if err != nil {
		panic(err)
	}
	var credentials credentialFile
	if err := yaml.Unmarshal(credentialRaw, &credentials); err != nil {
		panic(err)
	}
	password := credentials.Password
	if password == "" {
		password = credentials.Token
	}
	if password == "" {
		password = credentials.EventBusToken
	}
	if password == "" {
		password = credentials.MonitorEventBusToken
	}
	if credentials.Username == "" || password == "" {
		panic("credential file requires username and token/password")
	}
	caRaw, err := os.ReadFile(os.Args[3])
	if err != nil {
		panic(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caRaw) {
		panic("invalid CA")
	}
	conn, err := nats.Connect(os.Args[1], nats.UserInfo(credentials.Username, password), nats.Secure(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}), nats.TLSHandshakeFirst(), nats.Timeout(5*time.Second), nats.Name("moox-jetstream-inspect"))
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	if len(os.Args) == 5 && os.Args[4] == "delete-consumer" {
		js, err := conn.JetStream()
		if err != nil {
			panic(err)
		}
		if err := js.DeleteConsumer("MOOX_STORAGE", "collector-storage-write-v2-crypto"); err != nil {
			panic(err)
		}
		fmt.Println("consumer deleted=collector-storage-write-v2-crypto")
		return
	}
	if len(os.Args) == 5 && os.Args[4] == "ack-stale" {
		ackSubject := fmt.Sprintf("$JS.ACK.MOOX_STORAGE.collector-storage-write-v2-crypto.125063.282060.%d.1", time.Now().UnixNano())
		if err := conn.Publish(ackSubject, nil); err != nil {
			panic(err)
		}
		if err := conn.Flush(); err != nil {
			panic(err)
		}
		fmt.Printf("stale ack published stream=282060 consumer=125063\n")
		return
	}
	js, err := conn.JetStream()
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	fmt.Println("stream state query skipped: collector credential is intentionally not allowed to publish $JS.API.STREAM.INFO")
	if len(os.Args) == 5 && (os.Args[4] == "pull" || os.Args[4] == "pull-ack") {
		if os.Args[4] == "pull-ack" {
			_ = os.Setenv("MOOX_INSPECT_ACK", "1")
		}
		pullConsumer(js, ctx)
		return
	}
	if len(os.Args) == 5 {
		var sequence uint64
		if _, err := fmt.Sscanf(os.Args[4], "%d", &sequence); err != nil || sequence == 0 {
			panic("invalid stream sequence")
		}
		message, err := js.GetMsg("MOOX_STORAGE", sequence, nats.Context(ctx))
		if err != nil {
			panic(err)
		}
		fmt.Printf("message stream=%d subject=%s bytes=%d headers=", sequence, message.Subject, len(message.Data))
		for key := range message.Header {
			fmt.Printf("%s,", key)
		}
		fmt.Println()
		inspectStorageRows(&nats.Msg{Subject: message.Subject, Data: message.Data, Header: message.Header})
	}
	for _, name := range []string{"collector-storage-write-v2-crypto", "collector-market-fetch-crypto", "storage_view_kline", "storage_view_factor", "storage_view_metrics", "storage_view_misc"} {
		info, err := js.ConsumerInfo("MOOX_STORAGE", name, nats.Context(ctx))
		if err != nil {
			fmt.Printf("consumer %s error=%v\n", name, err)
			continue
		}
		lastActive := ""
		if info.Delivered.Last != nil {
			lastActive = info.Delivered.Last.UTC().Format(time.RFC3339)
		}
		fmt.Printf("consumer %s pending=%d ack_pending=%d redelivered=%d delivered_consumer=%d delivered_stream=%d ack_floor_consumer=%d ack_floor_stream=%d last_active=%s filter=%s filters=%s\n", name, info.NumPending, info.NumAckPending, info.NumRedelivered, info.Delivered.Consumer, info.Delivered.Stream, info.AckFloor.Consumer, info.AckFloor.Stream, lastActive, info.Config.FilterSubject, strings.Join(info.Config.FilterSubjects, ","))
	}
}

func pullConsumer(js nats.JetStreamContext, ctx context.Context) {
	const consumer = "collector-storage-write-v2-crypto"
	sub, err := js.PullSubscribe("moox.event.storage.dataset.rows.upserted.v2.mnzhs4dun4.>", consumer, nats.Bind("MOOX_STORAGE", consumer), nats.ManualAck())
	if err != nil {
		panic(err)
	}
	defer sub.Unsubscribe()
	messages, err := sub.Fetch(1, nats.Context(ctx))
	if err != nil {
		panic(err)
	}
	for _, message := range messages {
		metadata, metadataErr := message.Metadata()
		if metadataErr != nil {
			fmt.Printf("pull metadata error=%v\n", metadataErr)
		} else {
			fmt.Printf("pull stream=%d consumer=%d delivered=%d subject=%s bytes=%d reply=%s\n", metadata.Sequence.Stream, metadata.Sequence.Consumer, metadata.NumDelivered, message.Subject, len(message.Data), message.Reply)
		}
		inspectStorageRows(message)
		if os.Getenv("MOOX_INSPECT_ACK") == "1" {
			if err := message.Ack(nats.Context(ctx)); err != nil {
				panic(err)
			}
			fmt.Println("pull acked=true")
		}
	}
}

func inspectStorageRows(message *nats.Msg) {
	if message == nil {
		return
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		fmt.Printf("decoded error=%v\n", err)
		return
	}
	contentType := message.Header.Get("Content-Type")
	messageID := message.Header.Get("Nats-Msg-Id")
	envelope, payload, err := events.DecodeDatasetRowsUpsertedWithContentType(registry, message.Data, message.Subject, messageID, contentType)
	if err != nil {
		fmt.Printf("decoded error=%v\n", err)
		return
	}
	frequencies := map[string]int{}
	subjects := map[string]int{}
	minTime, maxTime := "", ""
	for _, row := range payload.GetRows() {
		if row == nil || row.GetKey() == nil || row.GetKey().GetTimeSeries() == nil {
			continue
		}
		key := row.GetKey().GetTimeSeries()
		frequencies[key.GetFreq()]++
		subjects[key.GetSubjectId()]++
		if minTime == "" || key.GetDataTime() < minTime {
			minTime = key.GetDataTime()
		}
		if maxTime == "" || key.GetDataTime() > maxTime {
			maxTime = key.GetDataTime()
		}
	}
	fmt.Printf("decoded event=%s@%d space=%s dataset=%s write_source=%s write_kind=%s source_node=%s source_store=%s source_sequence=%d rows=%d frequencies=%v subjects=%d data_time_min=%s data_time_max=%s\n",
		envelope.GetEventName(), envelope.GetEventVersion(), payload.GetSpaceId(), payload.GetDatasetId(), payload.GetWriteSource(), payload.GetWriteKind(), payload.GetSourceNodeId(), payload.GetSourceStoreId(), payload.GetSourceSequence(), len(payload.GetRows()), frequencies, len(subjects), minTime, maxTime)
}
