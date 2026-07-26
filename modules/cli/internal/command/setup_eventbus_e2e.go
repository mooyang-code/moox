package command

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/cloudjobqueue"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type eventBusE2EResult struct {
	PublicTLS           bool `json:"public_tls"`
	WorkerBindFetchAck  bool `json:"worker_bind_fetch_ack"`
	WorkerCreateDenied  bool `json:"worker_create_denied"`
	WorkerPublishDenied bool `json:"worker_publish_denied"`
}

func newSetupE2EEventBusCommand(deps setupDeps) *cobra.Command {
	var file string
	cmd := &cobra.Command{Use: "e2e-eventbus", Short: "从控制机外验证 EventBus TLS 和 CloudNode worker ACL", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		result, err := deps.e2eEventBus(cmd.Context(), snapshot)
		if err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, result)
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	return cmd
}

func defaultSetupE2EEventBus(ctx context.Context, snapshot *setupconfig.Snapshot) (eventBusE2EResult, error) {
	transport, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		return eventBusE2EResult{}, err
	}
	defer transport.Close()
	adminRaw, err := readRemoteEventBusFile(ctx, transport, ".config/moox/eventbus/internal-admin.yaml")
	if err != nil {
		return eventBusE2EResult{}, err
	}
	workerRaw, err := readRemoteEventBusFile(ctx, transport, ".config/moox/eventbus/cloudnode-worker.yaml")
	if err != nil {
		return eventBusE2EResult{}, err
	}
	caPEM, err := readRemoteEventBusFile(ctx, transport, ".config/moox/eventbus/ca.pem")
	if err != nil {
		return eventBusE2EResult{}, err
	}
	adminCredential, err := decodeEventBusCredential(adminRaw)
	if err != nil {
		return eventBusE2EResult{}, err
	}
	workerCredential, err := decodeEventBusCredential(workerRaw)
	if err != nil {
		return eventBusE2EResult{}, err
	}
	url := fmt.Sprintf("tls://%s:%d", snapshot.Manifest.EventBus.PublicAddress, snapshot.Manifest.EventBus.Port)
	connect := func(name string, credential jetstream.CredentialFile) (*jetstream.Client, error) {
		return jetstream.Connect(ctx, jetstream.Config{
			URLs: []string{url}, Name: name, Username: credential.Username, Password: credential.Password,
			TLSCAPEMBase64: base64.StdEncoding.EncodeToString([]byte(caPEM)), ConnectTimeout: 10 * time.Second,
		})
	}
	admin, err := connect("moox-setup-eventbus-admin-e2e", adminCredential)
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_public_tls_failed")
	}
	defer admin.Close()
	worker, err := connect("moox-setup-eventbus-worker-e2e", workerCredential)
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_worker_connect_failed")
	}
	defer worker.Close()

	registry, err := events.DefaultRegistry()
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_e2e_invalid")
	}
	identity := cloudjobqueue.Identity{SpaceID: "system", CodePackageID: "setup-probe", JobType: "eventbus.e2e"}
	consumerName, err := identity.ConsumerName()
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_e2e_invalid")
	}
	subjectID, err := identity.SubjectID()
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_e2e_invalid")
	}
	consumerConfig := events.SubjectConsumerConfig{
		ConsumerConfig: events.ConsumerConfig{
			Name: consumerName, Event: events.CloudJobExecutionRequested, AckWait: 30 * time.Second,
			MaxDeliver: 2, MaxAckPending: 1, FetchMaxWait: 10 * time.Second, DeliverPolicy: nats.DeliverNewPolicy,
		},
		SpaceID: "system", SubjectID: subjectID,
	}
	if _, err := events.EnsureSubjectConsumer(ctx, admin, registry, consumerConfig); err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_admin_prepare_failed")
	}
	defer admin.DeleteConsumer(context.Background(), events.CloudJobExecutionRequested.Stream(), consumerName)
	workerConsumer, err := events.BindSubjectConsumer(ctx, worker, registry, consumerConfig)
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_worker_bind_failed")
	}
	defer workerConsumer.Close()
	publisher, err := events.NewPublisher(admin, registry)
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_admin_prepare_failed")
	}
	eventID := fmt.Sprintf("setup-e2e-%d", time.Now().UnixNano())
	if _, err := publisher.Publish(ctx, events.CloudJobExecutionRequested, &cloudjobpb.JobExecutionRequested{
		JobId: eventID, JobItemId: eventID, JobType: identity.JobType, CodePackageId: identity.CodePackageID,
	}, events.PublishOptions{
		EventID: eventID, OccurredAt: time.Now().UTC(), SpaceID: identity.SpaceID, SubjectID: subjectID,
	}); err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_admin_publish_failed")
	}
	deliveries, err := workerConsumer.FetchEvents(ctx, 1)
	if err != nil || len(deliveries) != 1 || deliveries[0].Err != nil || deliveries[0].Message.GetEventId() != eventID {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_worker_fetch_failed")
	}
	if err := deliveries[0].Delivery.Ack(ctx); err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_worker_ack_failed")
	}

	forbidden := consumerConfig
	forbidden.Name += "_forbidden"
	_, createErr := events.EnsureSubjectConsumer(ctx, worker, registry, forbidden)
	workerPublisher, err := events.NewPublisher(worker, registry)
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_e2e_invalid")
	}
	_, publishErr := workerPublisher.Publish(ctx, events.CloudJobExecutionRequested, &cloudjobpb.JobExecutionRequested{
		JobId: eventID + "-forbidden", JobItemId: eventID + "-forbidden", JobType: identity.JobType, CodePackageId: identity.CodePackageID,
	}, events.PublishOptions{
		EventID: eventID + "-forbidden", OccurredAt: time.Now().UTC(), SpaceID: identity.SpaceID, SubjectID: subjectID,
	})
	result := eventBusE2EResult{
		PublicTLS: true, WorkerBindFetchAck: true,
		WorkerCreateDenied: createErr != nil, WorkerPublishDenied: publishErr != nil,
	}
	if !result.WorkerCreateDenied || !result.WorkerPublishDenied {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_worker_acl_failed")
	}
	return result, nil
}

func readRemoteEventBusFile(ctx context.Context, transport setupssh.Client, relativePath string) (string, error) {
	result, err := transport.Run(ctx, []string{"sh", "-lc", `cat "$HOME/$1"`, "moox-eventbus-e2e", relativePath}, nil)
	if err != nil || result.Stdout == "" || len(result.Stdout) > 1<<20 {
		return "", fmt.Errorf("eventbus_credentials_unavailable")
	}
	return result.Stdout, nil
}

func decodeEventBusCredential(raw string) (jetstream.CredentialFile, error) {
	var credential jetstream.CredentialFile
	if err := yaml.Unmarshal([]byte(raw), &credential); err != nil {
		return credential, fmt.Errorf("eventbus_credentials_invalid")
	}
	if credential.Password == "" {
		credential.Password = credential.Token
	}
	if credential.Password == "" {
		credential.Password = credential.EventBusToken
	}
	if strings.TrimSpace(credential.Username) == "" || credential.Password == "" {
		return credential, fmt.Errorf("eventbus_credentials_invalid")
	}
	return credential, nil
}
