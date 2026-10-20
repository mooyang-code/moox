//go:build windows

package servicehost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// install 注册服务：开机自动启动（延迟启动），异常退出 10 秒后重启（连续三次，一天后重新计数）。
func install(spec Spec) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager (run as administrator): %w", err)
	}
	defer m.Disconnect()
	if existing, err := m.OpenService(spec.Name); err == nil {
		existing.Close()
		return fmt.Errorf("service %s already exists; uninstall it first", spec.Name)
	}
	s, err := m.CreateService(spec.Name, exe, mgr.Config{
		DisplayName: spec.DisplayName, Description: spec.Description,
		StartType: mgr.StartAutomatic, DelayedAutoStart: true,
	}, spec.Args...)
	if err != nil {
		return fmt.Errorf("create service %s: %w", spec.Name, err)
	}
	defer s.Close()
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: RestartDelay},
		{Type: mgr.ServiceRestart, Delay: RestartDelay},
		{Type: mgr.ServiceRestart, Delay: RestartDelay},
	}
	if err := s.SetRecoveryActions(actions, uint32((24 * time.Hour).Seconds())); err != nil {
		_ = s.Delete()
		return fmt.Errorf("set recovery actions of %s: %w", spec.Name, err)
	}
	// 进程以非零码退出（而不是崩溃）也按失败处理并重启。
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		_ = s.Delete()
		return fmt.Errorf("set recovery on non-crash failures of %s: %w", spec.Name, err)
	}
	return nil
}

func uninstall(name string) error {
	return withService(name, func(s *mgr.Service) error {
		_, _ = s.Control(svc.Stop)
		return s.Delete()
	})
}

func start(name string) error {
	return withService(name, func(s *mgr.Service) error { return s.Start() })
}

func stop(name string) error {
	return withService(name, func(s *mgr.Service) error {
		status, err := s.Control(svc.Stop)
		if err != nil {
			return err
		}
		deadline := time.Now().Add(StopTimeout)
		for status.State != svc.Stopped {
			if time.Now().After(deadline) {
				return fmt.Errorf("service %s did not stop within %s", name, StopTimeout)
			}
			time.Sleep(300 * time.Millisecond)
			if status, err = s.Query(); err != nil {
				return err
			}
		}
		return nil
	})
}

func withService(name string, fn func(*mgr.Service) error) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager (run as administrator): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("open service %s: %w", name, err)
	}
	defer s.Close()
	return fn(s)
}

func run(name string, serve func(ctx context.Context) error) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isService {
		return serve(context.Background())
	}
	// 服务进程的工作目录是 System32；安装目录结构为 <安装目录>\bin\<程序>.exe，配置里的相对路径以安装目录为准。
	if exe, err := os.Executable(); err == nil {
		if err := os.Chdir(filepath.Dir(filepath.Dir(exe))); err != nil {
			return err
		}
	}
	h := &handler{serve: serve}
	if err := svc.Run(name, h); err != nil {
		return err
	}
	return h.err
}

type handler struct {
	serve func(ctx context.Context) error
	err   error
}

func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.serve(ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			h.err = err
			if err != nil && !errors.Is(err, context.Canceled) {
				// 非零退出码让服务管理器按恢复策略重启。
				return true, uint32(windows.ERROR_SERVICE_SPECIFIC_ERROR)
			}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				status <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case err := <-done:
					h.err = err
				case <-time.After(StopTimeout):
					h.err = fmt.Errorf("graceful shutdown exceeded %s", StopTimeout)
				}
				return false, 0
			}
		}
	}
}
