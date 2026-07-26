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
	ownerRaw, err := readRemoteEventBusFile(ctx, transport, ".config/moox/eventbus/cloudnode-eventbus.yaml")
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
	ownerCredential, err := decodeEventBusCredential(ownerRaw)
	if err != nil {
		return eventBusE2EResult{}, err
	}
	workerCredential, err := decodeEventBusCredential(workerRaw)
	if err != nil {
		return eventBusE2EResult{}, err
	}
	url := fmt.Sprintf("tls://%s:%d", snapshot.Manifest.EventBus.PublicAddress, snapshot.Manifest.EventBus.Port)
	connect := func(name string, credential jetstream.CredentialFile, onAsyncError func(error)) (*jetstream.Client, error) {
		return jetstream.Connect(ctx, jetstream.Config{
			URLs: []string{url}, Name: name, Username: credential.Username, Password: credential.Password,
			TLSCAPEMBase64: base64.StdEncoding.EncodeToString([]byte(caPEM)), ConnectTimeout: 10 * time.Second,
			AsyncErrorHandler: onAsyncError,
		})
	}
	admin, err := connect("moox-setup-eventbus-admin-e2e", adminCredential, func(error) {})
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_public_tls_failed")
	}
	defer admin.Close()
	owner, err := connect("moox-setup-eventbus-owner-e2e", ownerCredential, func(error) {})
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_owner_connect_failed")
	}
	defer owner.Close()
	workerAsyncErrors := make(chan error, 8)
	worker, err := connect("moox-setup-eventbus-worker-e2e", workerCredential, func(err error) {
		select {
		case workerAsyncErrors <- err:
		default:
		}
	})
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_worker_connect_failed")
	}
	defer worker.Close()

	registry, err := events.DefaultRegistry()
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_e2e_invalid")
	}
	eventID := fmt.Sprintf("setup-e2e-%d", time.Now().UnixNano())
	identity := cloudjobqueue.Identity{SpaceID: "system", CodePackageID: eventID, JobType: "eventbus.e2e"}
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
			MaxDeliver: 2, MaxAckPending: 1, FetchMaxWait: 10 * time.Second, DeliverPolicy: nats.DeliverAllPolicy,
		},
		SpaceID: "system", SubjectID: subjectID,
	}
	operationCtx, cancelOperation := context.WithTimeout(ctx, 30*time.Second)
	defer cancelOperation()
	if _, err := events.EnsureSubjectConsumer(operationCtx, owner, registry, consumerConfig); err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_owner_prepare_failed")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = admin.DeleteConsumer(cleanupCtx, events.CloudJobExecutionRequested.Stream(), consumerName)
	}()
	workerConsumer, err := events.BindSubjectConsumer(operationCtx, worker, registry, consumerConfig)
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_worker_bind_failed")
	}
	defer workerConsumer.Close()
	publisher, err := events.NewPublisher(owner, registry)
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_owner_prepare_failed")
	}
	if _, err := publisher.Publish(operationCtx, events.CloudJobExecutionRequested, &cloudjobpb.JobExecutionRequested{
		JobId: eventID, JobItemId: eventID, JobType: identity.JobType, CodePackageId: identity.CodePackageID,
	}, events.PublishOptions{
		EventID: eventID, OccurredAt: time.Now().UTC(), SpaceID: identity.SpaceID, SubjectID: subjectID,
	}); err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_owner_publish_failed")
	}
	deliveries, err := workerConsumer.FetchEvents(operationCtx, 1)
	if err != nil || len(deliveries) != 1 || deliveries[0].Err != nil || deliveries[0].Message.GetEventId() != eventID {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_worker_fetch_failed")
	}
	if err := deliveries[0].Delivery.Ack(operationCtx); err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_worker_ack_failed")
	}

	forbiddenConsumer := consumerName + "_forbidden"
	forbiddenCreateSubject := "$JS.API.CONSUMER.CREATE." +
		events.CloudJobExecutionRequested.Stream() + "." + forbiddenConsumer
	createDenialCtx, cancelCreateDenial := context.WithTimeout(ctx, 3*time.Second)
	createErr := worker.ProbePublishPermission(
		createDenialCtx,
		forbiddenCreateSubject,
		eventID+"-forbidden-create",
	)
	cancelCreateDenial()
	createDenied := createErr != nil && hasPermissionViolation(
		workerAsyncErrors,
		forbiddenCreateSubject,
	)
	workerPublisher, err := events.NewPublisher(worker, registry)
	if err != nil {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_e2e_invalid")
	}
	publishDenialCtx, cancelPublishDenial := context.WithTimeout(ctx, 3*time.Second)
	defer cancelPublishDenial()
	_, publishErr := workerPublisher.Publish(publishDenialCtx, events.CloudJobExecutionRequested, &cloudjobpb.JobExecutionRequested{
		JobId: eventID + "-forbidden", JobItemId: eventID + "-forbidden", JobType: identity.JobType, CodePackageId: identity.CodePackageID,
	}, events.PublishOptions{
		EventID: eventID + "-forbidden", OccurredAt: time.Now().UTC(), SpaceID: identity.SpaceID, SubjectID: subjectID,
	})
	publishDenied := publishErr != nil && hasPermissionViolation(workerAsyncErrors, "moox.cloudnode.job.execution.requested")
	result := eventBusE2EResult{
		PublicTLS: true, WorkerBindFetchAck: true,
		WorkerCreateDenied: createDenied, WorkerPublishDenied: publishDenied,
	}
	if !result.WorkerCreateDenied || !result.WorkerPublishDenied {
		return eventBusE2EResult{}, fmt.Errorf("eventbus_worker_acl_failed")
	}
	return result, nil
}

func hasPermissionViolation(errors <-chan error, subjectFragments ...string) bool {
	for {
		select {
		case err := <-errors:
			message := strings.ToLower(err.Error())
			if strings.Contains(message, "permissions violation") {
				for _, fragment := range subjectFragments {
					if strings.Contains(message, strings.ToLower(fragment)) {
						return true
					}
				}
			}
		default:
			return false
		}
	}
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
