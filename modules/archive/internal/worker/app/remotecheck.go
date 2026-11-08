package app

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"

	"github.com/mooyang-code/moox/modules/archive/internal/objectstore"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	"trpc.group/trpc-go/trpc-go/log"
)

// 每天维护时的远端抽查：按 remote_verified_at 从早到晚 HEAD 最多 remoteHeadPerDay 个已复制的分区，
// 再从通过的分区里随机 GET remoteGetPerDay 个重算 sha256。
const (
	remoteHeadPerDay = 100
	remoteGetPerDay  = 1
)

// 抽查发现问题时写进分区状态的原因。
const (
	reasonRemoteMissing  = "远端副本丢失"
	reasonRemoteMismatch = "远端副本内容不符"
)

// checkRemote 抽查远端副本。远端丢了或内容不符就重置成待上传（下一次运行从本地重传）；
// 网络、权限错误只记日志、不改状态（不能因为暂时连不上就把远端记成丢失），并结束这次抽查。结果随数据集下一次运行上报。
func (a *App) checkRemote(ctx context.Context) {
	remote := a.opts.Remote
	if remote == nil {
		return
	}
	refs, err := a.opts.Staging.ReplicatedPartitions(ctx, remoteHeadPerDay)
	if err != nil {
		log.Warnf("archive worker remote check: %v", err)
		return
	}
	var passed []staging.ReplicatedPartition
	for _, ref := range refs {
		if ctx.Err() != nil {
			return
		}
		p := ref.Partition
		head, err := remote.Head(ctx, p.Remote.Key)
		switch {
		case errors.Is(err, objectstore.ErrNotFound):
			// HEAD 的 404 没有正文，分不清是对象还是桶不存在：用 GET 再确认，桶不存在（配置错了）不能记成丢失。
			if _, _, gerr := remote.Get(ctx, p.Remote.Key, io.Discard); !errors.Is(gerr, objectstore.ErrNotFound) {
				log.Warnf("archive worker remote check stopped at %s: HEAD 404 but GET says %v", p.Remote.Key, gerr)
				return
			}
			a.resetRemote(ctx, ref, reasonRemoteMissing)
		case err != nil:
			log.Warnf("archive worker remote check stopped at %s: %v", p.Remote.Key, err)
			return
		case head.ETag != p.Remote.ETag || p.Remote.ETag == "":
			a.resetRemote(ctx, ref, reasonRemoteMismatch)
		default:
			// 大小在 HEAD 里核对不了本地文件（本地可能缺），以 ETag（内容 MD5）为准。
			if err := a.opts.Staging.MarkRemoteVerified(ctx, ref.SpaceID, ref.DatasetID, p.Key, a.opts.Now().UTC()); err != nil {
				log.Warnf("archive worker remote check %s: %v", p.Remote.Key, err)
				return
			}
			passed = append(passed, ref)
		}
	}
	for i := 0; i < remoteGetPerDay && len(passed) > 0; i++ {
		at := rand.IntN(len(passed))
		ref := passed[at]
		passed = append(passed[:at], passed[at+1:]...)
		_, sha, err := remote.Get(ctx, ref.Partition.Remote.Key, io.Discard)
		switch {
		case errors.Is(err, objectstore.ErrNotFound):
			a.resetRemote(ctx, ref, reasonRemoteMissing)
		case err != nil:
			log.Warnf("archive worker remote check download %s: %v", ref.Partition.Remote.Key, err)
		case sha != ref.Partition.SHA256:
			a.resetRemote(ctx, ref, reasonRemoteMismatch)
		}
	}
}

func (a *App) resetRemote(ctx context.Context, ref staging.ReplicatedPartition, reason string) {
	log.Warnf("archive worker remote check %s/%s %s: %s (%s), will upload it again", ref.SpaceID, ref.DatasetID, ref.Partition.Key, reason, ref.Partition.Remote.Key)
	if err := a.opts.Staging.ResetRemote(ctx, ref.SpaceID, ref.DatasetID, ref.Partition.Key, reason); err != nil {
		log.Warnf("archive worker reset remote %s/%s %s: %v", ref.SpaceID, ref.DatasetID, ref.Partition.Key, err)
	}
}
