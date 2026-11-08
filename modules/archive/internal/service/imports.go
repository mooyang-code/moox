package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/store"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
)

// importDirName 是归档根目录下存放待导入文件的目录（点开头：datasets.csv 扫描与启动清理都跳过它）。
const importDirName = ".import"

var (
	// importIDPattern 是 PrepareArchiveImport 生成的导入 ID。
	importIDPattern = regexp.MustCompile(`^imp_[0-9a-f]{24}$`)
	// importFilePattern 是导入文件名：字母或数字开头，只含字母、数字、点、下划线与连字符，以 .csv 或 .csv.gz 结尾。
	importFilePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

func newImportID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return "imp_" + hex.EncodeToString(raw[:])
}

// validateImportFile 校验导入 ID 与文件名，返回存进运行记录、下发给 worker 的 "<import_id>/<文件名>"。
func validateImportFile(importID, fileName string) (string, error) {
	if !importIDPattern.MatchString(importID) {
		return "", fmt.Errorf("import_id %q 无效：应是 PrepareArchiveImport 返回的值", importID)
	}
	if !importFilePattern.MatchString(fileName) || (!strings.HasSuffix(fileName, ".csv") && !strings.HasSuffix(fileName, ".csv.gz")) {
		return "", fmt.Errorf("文件名 %q 无效：只能含字母、数字、点、下划线与连字符，以 .csv 或 .csv.gz 结尾", fileName)
	}
	return importID + "/" + fileName, nil
}

// activeWorker 返回持有活动租约的 worker；没有时报错。
func (s *Service) activeWorker(ctx context.Context, now time.Time) (store.Worker, error) {
	workers, err := s.store.ListWorkers(ctx)
	if err != nil {
		return store.Worker{}, err
	}
	for _, w := range workers {
		if w.Active(now) {
			return w, nil
		}
	}
	return store.Worker{}, errors.New("没有活动的归档 worker，无法导入")
}

func (m *MgrService) PrepareArchiveImport(ctx context.Context, req *archivepb.PrepareArchiveImportReq) (*archivepb.PrepareArchiveImportRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.PrepareArchiveImportRsp{RetInfo: invalid(err)}, nil
	}
	if _, err := s.store.GetTask(ctx, space, req.GetDatasetId()); err != nil {
		return &archivepb.PrepareArchiveImportRsp{RetInfo: fail(err)}, nil
	}
	worker, err := s.activeWorker(ctx, s.clock())
	if err != nil {
		return &archivepb.PrepareArchiveImportRsp{RetInfo: conflict(err)}, nil
	}
	if !path.IsAbs(worker.RootDir) {
		return &archivepb.PrepareArchiveImportRsp{RetInfo: inner(fmt.Errorf("worker %s 上报的归档根目录 %q 不是绝对路径", worker.WorkerID, worker.RootDir))}, nil
	}
	id := newImportID()
	return &archivepb.PrepareArchiveImportRsp{
		RetInfo: success(), RootDir: worker.RootDir, WorkerId: worker.WorkerID, ImportId: id,
		ImportDir: path.Join(worker.RootDir, importDirName, id),
	}, nil
}

func (m *MgrService) ImportArchiveFile(ctx context.Context, req *archivepb.ImportArchiveFileReq) (*archivepb.ImportArchiveFileRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.ImportArchiveFileRsp{RetInfo: invalid(err)}, nil
	}
	file, err := validateImportFile(req.GetImportId(), req.GetFileName())
	if err != nil {
		return &archivepb.ImportArchiveFileRsp{RetInfo: invalid(err)}, nil
	}
	run, err := s.store.CreateRun(ctx, space, req.GetDatasetId(), store.RunSpec{Kind: store.KindImport, ImportFile: file, Overwrite: req.GetOverwrite()}, s.clock())
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return &archivepb.ImportArchiveFileRsp{RetInfo: conflict(errors.New("任务已有排队中或运行中的运行，等它结束后再导入"))}, nil
		}
		return &archivepb.ImportArchiveFileRsp{RetInfo: fail(err)}, nil
	}
	return &archivepb.ImportArchiveFileRsp{RetInfo: success(), Run: runToPB(run)}, nil
}

func (m *MgrService) ClearArchiveUnrecoverable(ctx context.Context, req *archivepb.ClearArchiveUnrecoverableReq) (*archivepb.ClearArchiveUnrecoverableRsp, error) {
	s := m.svc
	space, err := resolveSpace(ctx, req.GetSpaceId(), true)
	if err != nil {
		return &archivepb.ClearArchiveUnrecoverableRsp{RetInfo: invalid(err)}, nil
	}
	start, err := parseRFC("start", req.GetStart())
	var end time.Time
	if err == nil {
		end, err = parseRFC("end", req.GetEnd())
	}
	if err == nil && (start.IsZero() || end.IsZero() || !start.Before(end)) {
		err = errors.New("start、end 都必填，且 start 必须早于 end")
	}
	if err != nil {
		return &archivepb.ClearArchiveUnrecoverableRsp{RetInfo: invalid(err)}, nil
	}
	removed, err := s.store.ClearUnrecoverable(ctx, space, req.GetDatasetId(), start, end)
	if err != nil {
		return &archivepb.ClearArchiveUnrecoverableRsp{RetInfo: fail(err)}, nil
	}
	rsp := &archivepb.ClearArchiveUnrecoverableRsp{RetInfo: success()}
	for _, item := range removed {
		rsp.Removed = append(rsp.Removed, &archivepb.UnrecoverableRange{SubjectId: item.SubjectID, SeriesTag: item.SeriesTag, StartTime: item.StartTime, EndTime: item.EndTime})
	}
	return rsp, nil
}
