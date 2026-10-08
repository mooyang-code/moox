package rpc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/tencentyun/cos-go-sdk-v5"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// packageGarbageGrace keeps an unused package long enough for the publish
	// that uploaded it to create or deploy its nodes. Orphaned COS objects use
	// the same grace so an upload in progress is never removed.
	packageGarbageGrace = 6 * time.Hour
	// deletedNodeRetention keeps soft-deleted node rows long enough for a
	// retried delete to stay idempotent.
	deletedNodeRetention = 24 * time.Hour
	// nodeBatchRetention keeps finished batches for publish diagnosis.
	nodeBatchRetention = 7 * 24 * time.Hour
	// packageObjectPrefix is the only COS prefix CloudNode writes; garbage
	// collection never touches other objects in the bucket.
	packageObjectPrefix = "moox/cloud-packages/"
)

type packageObject struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// packageObjectStore is the COS surface garbage collection needs.
type packageObjectStore interface {
	List(ctx context.Context, prefix string) ([]packageObject, error)
	Delete(ctx context.Context, key string) error
}

// CollectGarbage removes CloudNode data that nothing uses any more: packages
// no live node runs (their COS objects included), COS objects no package
// record references, soft-deleted nodes and finished node batches.
func (s *Service) CollectGarbage(ctx context.Context, req *pb.CollectGarbageReq) (*pb.CollectGarbageRsp, error) {
	now := time.Now().UTC()
	gc := &garbageCollection{service: s, dryRun: req.GetDryRun(), rsp: &pb.CollectGarbageRsp{}, stores: map[string]packageObjectStore{}}
	if err := gc.packages(ctx, now.Add(-packageGarbageGrace)); err != nil {
		return &pb.CollectGarbageRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if err := gc.orphanObjects(ctx, now.Add(-packageGarbageGrace)); err != nil {
		return &pb.CollectGarbageRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	nodes, err := s.catalog.PurgeDeletedNodes(ctx, now.Add(-deletedNodeRetention), gc.dryRun)
	if err != nil {
		return &pb.CollectGarbageRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "purge deleted nodes: "+err.Error())}, nil
	}
	gc.rsp.DeletedNodes = uint32(nodes)
	batches, err := s.catalog.PurgeFinishedNodeBatches(ctx, now.Add(-nodeBatchRetention), gc.dryRun)
	if err != nil {
		return &pb.CollectGarbageRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "purge finished node batches: "+err.Error())}, nil
	}
	gc.rsp.NodeBatches = uint32(batches)
	gc.rsp.RetInfo = retOK()
	log.InfoContextf(ctx, "[CloudNode-GC] dry_run=%t packages=%d cos_objects=%d cos_bytes=%d deleted_nodes=%d node_batches=%d skipped=%q",
		gc.dryRun, gc.rsp.Packages, gc.rsp.CosObjects, gc.rsp.CosBytes, gc.rsp.DeletedNodes, gc.rsp.NodeBatches, gc.rsp.Skipped)
	return gc.rsp, nil
}

type garbageCollection struct {
	service *Service
	dryRun  bool
	rsp     *pb.CollectGarbageRsp
	// stores caches one COS client per cloud account; a nil entry records an
	// account whose bucket cannot be reached in this run.
	stores map[string]packageObjectStore
}

func (gc *garbageCollection) skip(format string, args ...any) {
	gc.rsp.Skipped = append(gc.rsp.Skipped, fmt.Sprintf(format, args...))
}

func (gc *garbageCollection) packages(ctx context.Context, cutoff time.Time) error {
	active, err := gc.service.catalog.HasActiveNodeBatches(ctx)
	if err != nil {
		return fmt.Errorf("check active node batches: %w", err)
	}
	if active {
		// A running batch may be deploying any package, including one that no
		// node runs yet; collect packages on the next run.
		gc.skip("packages: a node batch is still running")
		return nil
	}
	packages, err := gc.service.catalog.ListGarbagePackages(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("list unused packages: %w", err)
	}
	for _, pkg := range packages {
		hasObject := pkg.COSBucket != "" && pkg.COSPath != ""
		var objects packageObjectStore
		if hasObject {
			// Resolve COS before dropping the record, so an unreachable bucket
			// keeps the record that points at its object.
			if objects = gc.store(ctx, pkg.CloudAccountID); objects == nil {
				continue
			}
		}
		if gc.dryRun {
			gc.countPackage(pkg, hasObject)
			continue
		}
		purged, err := gc.service.catalog.PurgePackage(ctx, pkg)
		if err != nil {
			return fmt.Errorf("purge package %s: %w", pkg.PackageID, err)
		}
		if !purged {
			continue
		}
		gc.rsp.Packages++
		if !hasObject {
			continue
		}
		// A failed delete leaves an orphaned object, which the orphan sweep
		// removes on a later run.
		if err := objects.Delete(ctx, strings.TrimLeft(pkg.COSPath, "/")); err != nil {
			gc.skip("package %s: delete COS object: %v", pkg.PackageID, err)
			continue
		}
		gc.rsp.CosObjects++
		gc.rsp.CosBytes += uint64(max(pkg.FileSize, 0))
	}
	return nil
}

func (gc *garbageCollection) countPackage(pkg store.FunctionPackage, hasObject bool) {
	gc.rsp.Packages++
	if hasObject {
		gc.rsp.CosObjects++
		gc.rsp.CosBytes += uint64(max(pkg.FileSize, 0))
	}
}

func (gc *garbageCollection) orphanObjects(ctx context.Context, cutoff time.Time) error {
	referenced, err := gc.service.catalog.PackageObjectKeys(ctx)
	if err != nil {
		return fmt.Errorf("list package object keys: %w", err)
	}
	accounts, _, err := gc.service.catalog.ListAccounts(ctx, "")
	if err != nil {
		return fmt.Errorf("list cloud accounts: %w", err)
	}
	for _, account := range accounts {
		if account.COSBucket == "" || account.COSRegion == "" {
			continue
		}
		objects := gc.store(ctx, account.AccountID)
		if objects == nil {
			continue
		}
		listed, err := objects.List(ctx, packageObjectPrefix)
		if err != nil {
			gc.skip("account %s: list COS objects: %v", account.AccountID, err)
			continue
		}
		for _, object := range listed {
			if _, ok := referenced[account.COSBucket][object.Key]; ok || !object.LastModified.Before(cutoff) {
				continue
			}
			if !gc.dryRun {
				if err := objects.Delete(ctx, object.Key); err != nil {
					gc.skip("account %s: delete orphaned COS object %s: %v", account.AccountID, object.Key, err)
					continue
				}
			}
			gc.rsp.CosObjects++
			gc.rsp.CosBytes += uint64(max(object.Size, 0))
		}
	}
	return nil
}

func (gc *garbageCollection) store(ctx context.Context, accountID string) packageObjectStore {
	if objects, ok := gc.stores[accountID]; ok {
		return objects
	}
	objects, err := gc.service.packageObjectStore(ctx, accountID)
	if err != nil {
		gc.skip("account %s: %v", accountID, err)
	}
	gc.stores[accountID] = objects
	return objects
}

func (s *Service) packageObjectStore(ctx context.Context, accountID string) (packageObjectStore, error) {
	account, err := s.catalog.GetAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("load cloud account: %w", err)
	}
	if account == nil || account.COSBucket == "" || account.COSRegion == "" {
		return nil, fmt.Errorf("cloud account has no COS bucket")
	}
	credential, err := s.resolveCloudCredential(ctx, *account)
	if err != nil {
		return nil, fmt.Errorf("resolve cloud credential: %w", err)
	}
	return s.packageObjectsFactory(*account, credential), nil
}

func defaultPackageObjectsFactory(account store.CloudAccount, credential cloudcredential.TencentCredential) packageObjectStore {
	return cosPackageObjects{client: newCOSClient(account, credential)}
}

type cosPackageObjects struct{ client *cos.Client }

func (c cosPackageObjects) List(ctx context.Context, prefix string) ([]packageObject, error) {
	var objects []packageObject
	marker := ""
	for {
		result, _, err := c.client.Bucket.Get(ctx, &cos.BucketGetOptions{Prefix: prefix, Marker: marker, MaxKeys: 1000})
		if err != nil {
			return nil, err
		}
		for _, object := range result.Contents {
			modified, err := time.Parse(time.RFC3339, object.LastModified)
			if err != nil {
				return nil, fmt.Errorf("COS object %s has invalid LastModified %q", object.Key, object.LastModified)
			}
			objects = append(objects, packageObject{Key: object.Key, Size: object.Size, LastModified: modified.UTC()})
		}
		if !result.IsTruncated {
			return objects, nil
		}
		marker = result.NextMarker
		if marker == "" && len(result.Contents) > 0 {
			marker = result.Contents[len(result.Contents)-1].Key
		}
	}
}

func (c cosPackageObjects) Delete(ctx context.Context, key string) error {
	if _, err := c.client.Object.Delete(ctx, key); err != nil && !cos.IsNotFoundError(err) {
		return err
	}
	return nil
}
