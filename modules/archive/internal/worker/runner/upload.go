package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/objectstore"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/export"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	"trpc.group/trpc-go/trpc-go/log"
)

// backlogUploadBudget 是一次运行里补传之前遗留的待上传分区最多花的时间（从本次运行第一次上传起累计）。
// 首次启用、原地升级或长时间断网后的积压分摊到之后的每次运行；本次运行导出的分区不受它限制。
const backlogUploadBudget = 2 * time.Minute

// errLocalMismatch 是本地文件与记录不一致的失败原因：不上传，需要人工核对。
const errLocalMismatch = "本地文件与记录不一致，需要人工核对"

// uploadState 是一次运行里的上传进度。
type uploadState struct {
	// backlogSince 是本次运行第一次调用 uploadPending 的时刻；零值表示还没调用过。
	backlogSince time.Time
	// stopped：本次运行已经有一次远端失败（多半是网络或权限问题），不再传后面的分区，免得每个分区各等一次超时。
	stopped  bool
	failed   int
	firstErr string
}

// uploadPending 把本数据集待上传的分区传到对象存储：读文件算 MD5 与 sha256 → 单次 PUT → HEAD 核对大小与 ETag。
// 失败只记进分区状态（退避后重试），不让运行失败。有分区上传成功时，dataset.json 内容变了也传一份。
func (s *runState) uploadPending(ctx context.Context, out *Outcome) {
	remote := s.r.Remote
	if remote == nil || s.upload.stopped || ctx.Err() != nil {
		return
	}
	now := s.r.now()
	if s.upload.backlogSince.IsZero() {
		s.upload.backlogSince = now
	}
	pending, err := s.r.Staging.PendingUploads(ctx, s.task.SpaceID, s.task.DatasetID, now)
	if err != nil {
		s.recordFailure(fmt.Sprintf("列出待上传分区: %v", err))
		return
	}
	uploaded := 0
	for _, p := range pending {
		if ctx.Err() != nil || s.upload.stopped {
			break
		}
		if !s.exported[p.Key] && s.r.now().Sub(s.upload.backlogSince) >= backlogUploadBudget {
			continue // 补传预算用完：遗留的分区留给之后的运行
		}
		ok, remoteFailure, reason := s.uploadOne(ctx, p)
		if ok {
			uploaded++
			out.Uploaded++
			continue
		}
		if ctx.Err() != nil {
			// 取消不算失败，不记退避。
			break
		}
		if reason == "" {
			continue // 上传期间被重导出：既不算成功也不算失败
		}
		s.recordFailure(reason)
		if err := s.r.Staging.MarkUploadFailed(context.WithoutCancel(ctx), s.task.SpaceID, s.task.DatasetID, p.Key, p.SHA256, reason, s.r.now()); err != nil {
			log.Warnf("archive worker mark upload failed %s/%s %s: %v", s.task.SpaceID, s.task.DatasetID, p.Key, err)
		}
		if remoteFailure {
			s.upload.stopped = true
		}
	}
	if uploaded > 0 {
		s.uploadDatasetMeta(ctx)
	}
}

func (s *runState) recordFailure(reason string) {
	s.upload.failed++
	if s.upload.firstErr == "" {
		s.upload.firstErr = reason
	}
}

// uploadOne 上传一个分区。返回是否成功；失败时 remoteFailure 表示对象存储一侧的失败（网络、权限、校验），
// 本地文件缺失或与记录不一致不算（不影响后面的分区）。
func (s *runState) uploadOne(ctx context.Context, p staging.PartitionState) (ok, remoteFailure bool, reason string) {
	remote := s.r.Remote
	key, err := objectstore.VersionedKey(p.FilePath, p.SHA256)
	if err != nil {
		return false, false, objectstore.ErrorText(err)
	}
	file, err := os.Open(s.r.Files.Abs(p.FilePath))
	if errors.Is(err, os.ErrNotExist) {
		return false, false, "本地文件不存在"
	}
	if err != nil {
		return false, false, objectstore.ErrorText(fmt.Errorf("打开本地文件: %w", err))
	}
	defer file.Close()
	size, sums, err := objectstore.Digest(file)
	if err != nil {
		return false, false, objectstore.ErrorText(fmt.Errorf("读取本地文件: %w", err))
	}
	if sums.SHA256 != p.SHA256 {
		return false, false, errLocalMismatch
	}
	uploadedAt := s.r.now()
	etag, err := remote.Put(ctx, key, file, size, sums, objectstore.Meta{
		ContentType: objectstore.ContentType(p.FilePath), SHA256: sums.SHA256, Dataset: s.task.DatasetID, Partition: p.Key,
	})
	if err != nil {
		return false, !errors.Is(err, objectstore.ErrTooLarge), objectstore.ErrorText(err)
	}
	head, err := remote.Head(ctx, key)
	if err != nil {
		return false, true, objectstore.ErrorText(err)
	}
	// 单次上传的 ETag 就是内容 MD5。
	if head.Size != size || head.ETag != sums.MD5 || etag != sums.MD5 {
		return false, true, fmt.Sprintf("上传 %s 后核对不符：远端大小 %d、ETag %s，本地大小 %d、MD5 %s", key, head.Size, head.ETag, size, sums.MD5)
	}
	marked, err := s.r.Staging.MarkUploaded(context.WithoutCancel(ctx), s.task.SpaceID, s.task.DatasetID, p.Key, p.SHA256,
		staging.Remote{Key: key, ETag: head.ETag, UploadedAt: uploadedAt, VerifiedAt: s.r.now()})
	if err != nil {
		return false, false, objectstore.ErrorText(fmt.Errorf("记录上传结果: %w", err))
	}
	// 上传期间被重导出：不记成已上传，新版本留给下一次上传（不算失败）。
	return marked, false, ""
}

// uploadDatasetMeta 在 dataset.json 内容变了时传到 <space>/<dataset_id>/dataset.json（覆盖旧的；它是说明文件，可以重新生成）。
// 失败只记日志，下次再试，不进分区状态，不影响健康等级。
func (s *runState) uploadDatasetMeta(ctx context.Context) {
	rel := path.Join(s.task.SpaceID, s.task.DatasetID, "dataset.json")
	file, err := os.Open(s.r.Files.Abs(rel))
	if err != nil {
		log.Warnf("archive worker upload dataset.json %s: %v", rel, err)
		return
	}
	defer file.Close()
	size, sums, err := objectstore.Digest(file)
	if err != nil {
		log.Warnf("archive worker upload dataset.json %s: %v", rel, err)
		return
	}
	current, err := s.r.Staging.MetaRemoteSHA(ctx, s.task.SpaceID, s.task.DatasetID)
	if err != nil || current == sums.SHA256 {
		return
	}
	etag, err := s.r.Remote.Put(ctx, rel, file, size, sums, objectstore.Meta{ContentType: objectstore.ContentType(rel), SHA256: sums.SHA256, Dataset: s.task.DatasetID})
	if err == nil && etag != sums.MD5 {
		err = fmt.Errorf("ETag %s 与 MD5 %s 不一致", etag, sums.MD5)
	}
	if err != nil {
		log.Warnf("archive worker upload dataset.json %s: %v", rel, err)
		return
	}
	if err := s.r.Staging.SetMetaRemoteSHA(context.WithoutCancel(ctx), s.task.SpaceID, s.task.DatasetID, sums.SHA256); err != nil {
		log.Warnf("archive worker record dataset.json upload %s: %v", rel, err)
	}
}

// fetchFinal 在读回已定稿分区、本地文件缺失或 sha256 不符（verifyErr）时，从对象存储取回当前版本。
// 只处理这两种情况，权限、读盘等错误照旧返回（要人看，取回可能掩盖坏盘）。sha256 不符时先把本地文件改名为 .bad-<unix 秒> 保留。
func (s *runState) fetchFinal(ctx context.Context, p staging.PartitionState, verifyErr error) error {
	missing := errors.Is(verifyErr, export.ErrFileMissing)
	if !missing && !errors.Is(verifyErr, export.ErrChecksumMismatch) {
		return verifyErr
	}
	if s.r.Remote == nil || !p.RemoteIsCurrent() {
		if missing {
			return fmt.Errorf("本地文件缺失且没有可用的远端副本: %w", verifyErr)
		}
		return verifyErr
	}
	if !missing {
		bad, err := s.r.Files.Quarantine(p.FilePath, s.r.now())
		if err != nil {
			return fmt.Errorf("保留校验不符的本地文件: %w", err)
		}
		log.Warnf("archive worker kept mismatched file %s as %s before fetching it from the object store", p.FilePath, bad)
	}
	err := s.r.Files.Restore(p.FilePath, p.SHA256, func(w io.Writer) error {
		_, _, err := s.r.Remote.Get(ctx, p.Remote.Key, w)
		return err
	})
	if err != nil {
		return fmt.Errorf("从对象存储取回 %s: %w", p.Remote.Key, err)
	}
	s.notes = append(s.notes, fmt.Sprintf("分区 %s 已从对象存储取回", p.Key))
	return nil
}

// successMessage 是成功运行的消息：上传失败的分区数与第一条错误，以及取回说明。
func (s *runState) successMessage() string {
	var parts []string
	if s.upload.failed > 0 {
		parts = append(parts, fmt.Sprintf("%d 个分区上传对象存储失败：%s", s.upload.failed, s.upload.firstErr))
	}
	parts = append(parts, s.notes...)
	return strings.Join(parts, "；")
}
