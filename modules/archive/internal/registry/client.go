package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"trpc.group/trpc-go/trpc-go/client"
)

type MetadataClient interface {
	RegisterArchiveFile(context.Context, *storagepb.RegisterArchiveFileReq, ...client.Option) (*storagepb.RegisterArchiveFileRsp, error)
}
type Client struct {
	proxy storagepb.MetadataClientProxy
	mu    sync.Mutex
	last  map[string]uint64
}

type PartitionRegistry struct {
	Client   *Client
	DeviceID string
}

func (r PartitionRegistry) Register(ctx context.Context, key domain.PartitionKey, manifest domain.Manifest) error {
	if r.Client == nil {
		return fmt.Errorf("metadata client is nil")
	}
	return r.Client.Register(ctx, BuildArchiveFile(r.DeviceID, key, manifest, false, domain.COSState{}))
}

func (r PartitionRegistry) RegisterCOS(ctx context.Context, key domain.PartitionKey, manifest domain.Manifest, cosState domain.COSState) error {
	if r.Client == nil {
		return fmt.Errorf("metadata client is nil")
	}
	return r.Client.Register(ctx, BuildArchiveFile(r.DeviceID, key, manifest, false, cosState))
}

func (c *Client) RegisterPartition(ctx context.Context, key domain.PartitionKey, manifest domain.Manifest) error {
	return c.Register(ctx, BuildArchiveFile("parquet-local", key, manifest, false, domain.COSState{}))
}

func NewClientWithCredentials(target, targetNode string, credentials gatewayauth.Credentials) *Client {
	return &Client{proxy: storagepb.NewMetadataClientProxy(gatewayauth.NewTRPCClientOptions(target, targetNode, credentials)...)}
}

func StableArchiveFileID(key domain.PartitionKey) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{key.SpaceID, key.DatasetID, key.Freq, key.SubjectID, key.SeriesTag, key.Month}, "\n")))
	return hex.EncodeToString(sum[:])
}

func BuildArchiveFile(deviceID string, key domain.PartitionKey, manifest domain.Manifest, sealed bool, cosState domain.COSState) *storagepb.ArchiveFile {
	status := "active"
	if sealed {
		status = "sealed"
	}
	attrs := map[string]string{"schema_version": "2", "generation": fmt.Sprint(manifest.Generation), "sha256": manifest.SHA256, "cos_status": cosState.Status, "cos_object_key": cosState.ObjectKey}
	raw, _ := json.Marshal(attrs)
	fileURL := (&url.URL{Scheme: "file", Path: manifest.Path}).String()
	columns := []string{"candle_begin_time", "space_id", "dataset_id", "subject_id", "freq", "series_tag", "attributes_json", "written_at"}
	columns = append(columns, manifest.Columns...)
	return &storagepb.ArchiveFile{SpaceId: key.SpaceID, ArchiveFileId: StableArchiveFileID(key), DatasetId: key.DatasetID, DeviceId: deviceID, PartitionKey: fmt.Sprintf("%s/%s/series_tag=%s/%s", key.Freq, domain.EncodeIdentity(key.SubjectID), domain.EncodeIdentity(key.SeriesTag), key.Month), FileUri: fileURL, FileFormat: "parquet", MinTime: manifest.MinTime.UTC().Format(time.RFC3339Nano), MaxTime: manifest.MaxTime.UTC().Format(time.RFC3339Nano), RowCount: manifest.RowCount, Columns: columns, Status: status, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Attributes: map[string]string{"schema_version": "2", "generation": fmt.Sprint(manifest.Generation), "cos_status": cosState.Status, "cos_object_key": cosState.ObjectKey, "manifest": string(raw), "content_hash": manifest.SHA256}}
}

func (c *Client) Register(ctx context.Context, file *storagepb.ArchiveFile) error {
	if c == nil || c.proxy == nil {
		return fmt.Errorf("metadata client is nil")
	}
	generation, err := strconv.ParseUint(file.GetAttributes()["generation"], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid archive generation: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		c.last = map[string]uint64{}
	}
	if generation < c.last[file.GetArchiveFileId()] {
		return fmt.Errorf("refusing to roll archive registry generation backward")
	}
	rsp, err := c.proxy.RegisterArchiveFile(ctx, &storagepb.RegisterArchiveFileReq{ArchiveFile: file})
	if err != nil {
		return err
	}
	if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return fmt.Errorf("metadata register archive file failed")
	}
	c.last[file.GetArchiveFileId()] = generation
	return nil
}
