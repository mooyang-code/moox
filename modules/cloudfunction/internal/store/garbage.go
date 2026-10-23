package store

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Garbage collection keeps every mutation a single statement (or a
// transaction that starts with a write), so it takes the SQLite write lock
// before reading. A deferred read-then-write transaction fails with
// SQLITE_BUSY_SNAPSHOT in WAL mode once a concurrent writer commits.

var (
	activeFunctionJobStatuses   = []string{FunctionJobPending, FunctionJobRunning, FunctionJobReconciliationRequired}
	finishedFunctionJobStatuses = []string{FunctionJobSuccess, FunctionJobFailed, FunctionJobPartial}
)

// garbageCutoff formats a cutoff like the stored UTC timestamps
// ("2006-01-02 15:04:05[.fraction][+00:00]") so string comparison orders them.
func garbageCutoff(cutoff time.Time) string {
	return cutoff.UTC().Format("2006-01-02 15:04:05")
}

// unusedPackageCondition matches packages that no live instance runs.
const unusedPackageCondition = `NOT EXISTS (
	SELECT 1 FROM t_cloud_function_instances AS instance
	WHERE instance.c_is_deleted = ? AND instance.c_space_id = t_cloud_function_packages.c_space_id
	  AND instance.c_package_id = t_cloud_function_packages.c_package_id
)`

// HasActiveFunctionJobs reports whether a instance batch may still deploy a package.
func (r *CatalogRepository) HasActiveFunctionJobs(ctx context.Context) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&FunctionJob{}).Where("c_status IN ?", activeFunctionJobStatuses).Count(&count).Error
	return count > 0, err
}

// ListGarbagePackages returns packages that no live instance runs and that were
// deleted or created before cutoff.
func (r *CatalogRepository) ListGarbagePackages(ctx context.Context, cutoff time.Time) ([]FunctionPackage, error) {
	var packages []FunctionPackage
	err := r.db.WithContext(ctx).
		Where("(c_is_deleted = ? OR c_ctime < ?)", true, garbageCutoff(cutoff)).
		Where(unusedPackageCondition, false).
		Order("c_id ASC").Find(&packages).Error
	return packages, err
}

// PurgePackage removes a package record unless a live instance started running it
// after it was listed.
func (r *CatalogRepository) PurgePackage(ctx context.Context, pkg FunctionPackage) (bool, error) {
	result := r.db.WithContext(ctx).Where("c_id = ?", pkg.ID).Where(unusedPackageCondition, false).Delete(&FunctionPackage{})
	return result.RowsAffected == 1, result.Error
}

// PackageObjectKeys returns the COS keys that package records still reference,
// grouped by bucket.
func (r *CatalogRepository) PackageObjectKeys(ctx context.Context) (map[string]map[string]struct{}, error) {
	var packages []FunctionPackage
	if err := r.db.WithContext(ctx).Select("c_cos_bucket", "c_cos_path").Find(&packages).Error; err != nil {
		return nil, err
	}
	keys := make(map[string]map[string]struct{})
	for _, pkg := range packages {
		if pkg.COSBucket == "" || pkg.COSPath == "" {
			continue
		}
		if keys[pkg.COSBucket] == nil {
			keys[pkg.COSBucket] = make(map[string]struct{})
		}
		keys[pkg.COSBucket][packageObjectKey(pkg.COSPath)] = struct{}{}
	}
	return keys, nil
}

// PurgeDeletedInstances removes soft-deleted instance rows last changed before cutoff.
// With dryRun it only counts them.
func (r *CatalogRepository) PurgeDeletedInstances(ctx context.Context, cutoff time.Time, dryRun bool) (int64, error) {
	query := r.db.WithContext(ctx).Model(&FunctionInstance{}).Where("c_is_deleted = ? AND c_mtime < ?", true, garbageCutoff(cutoff))
	if dryRun {
		var count int64
		err := query.Count(&count).Error
		return count, err
	}
	result := query.Delete(&FunctionInstance{})
	return result.RowsAffected, result.Error
}

// PurgeFinishedFunctionJobs removes instance batches that finished before cutoff,
// together with their items. A batch whose item still owes a publish lease
// release is kept for the release worker. With dryRun it only counts them.
func (r *CatalogRepository) PurgeFinishedFunctionJobs(ctx context.Context, cutoff time.Time, dryRun bool) (int64, error) {
	finished := r.db.Model(&FunctionJob{}).Select("c_space_id", "c_job_id").
		Where("c_status IN ? AND c_completed_at IS NOT NULL AND c_completed_at < ?", finishedFunctionJobStatuses, garbageCutoff(cutoff)).
		Where(`NOT EXISTS (
			SELECT 1 FROM t_cloud_function_job_items AS item
			WHERE item.c_space_id = t_cloud_function_jobs.c_space_id AND item.c_job_id = t_cloud_function_jobs.c_job_id
			  AND item.c_publish_lease_recovery_owned = ?
		)`, true)
	if dryRun {
		var count int64
		err := r.db.WithContext(ctx).Table("(?) AS finished", finished).Count(&count).Error
		return count, err
	}
	var purged int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("(c_space_id, c_job_id) IN (?)", finished).Delete(&FunctionJobItem{}).Error; err != nil {
			return err
		}
		result := tx.Where("(c_space_id, c_job_id) IN (?)", finished).Delete(&FunctionJob{})
		purged = result.RowsAffected
		return result.Error
	})
	return purged, err
}

func packageObjectKey(cosPath string) string {
	return strings.TrimLeft(cosPath, "/")
}
