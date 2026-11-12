package logsink

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/plugin"
)

// configDecoder 模拟 tRPC 把 plugins.log.default 的配置交给日志装配器。
type configDecoder struct{ outputs log.Config }

func (d configDecoder) Decode(target interface{}) error {
	cfg, ok := target.(*log.Config)
	if !ok {
		return fmt.Errorf("unexpected decode target %T", target)
	}
	*cfg = append(log.Config(nil), d.outputs...)
	return nil
}

// 调用 InstallDefaultFactory 之后，默认日志按配置文件装配时会自动带上 moox 输出：配置文件里不用写它，
// 所以只换二进制、不改线上配置也能生效。装配后的日志经 tRPC 的包级函数写出时，代码位置仍是调用处。
func TestInstalledFactoryAddsTheSinkWithoutConfigChanges(t *testing.T) {
	previous := log.GetDefaultLogger()
	// 本进程的输出是全局的：测试前后都清掉，重复运行时互不影响。
	current.Store(nil)
	t.Cleanup(func() {
		log.SetLogger(previous)
		current.Store(nil)
	})

	InstallDefaultFactory()
	factory := plugin.Get("log", "default")
	if _, ok := factory.(defaultFactory); !ok {
		t.Fatalf("默认日志的装配器 = %T，应已被接管", factory)
	}
	// 配置里只有控制台输出，没有 moox。
	if err := factory.Setup("default", configDecoder{outputs: log.Config{{Writer: "console", Level: "fatal"}}}); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if !Enabled() {
		t.Fatal("装配之后应已启用")
	}
	target := &collected{}
	if !Attach(identity, target.deliver) {
		t.Fatal("Attach 应当生效")
	}

	_, file, line, _ := runtime.Caller(0)
	log.Errorf("写入失败 %d", 42)
	log.Info("普通信息不进重要日志")
	Important(context.Background(), "process_started", "启动 %s", "v1")
	current.Load().flush()

	got := target.records()
	if len(got) != 2 {
		t.Fatalf("重要日志 = %+v，应为 2 条", got)
	}
	if want := fmt.Sprintf("logsink/%s:%d", filepath.Base(file), line+1); got[0].Message != "写入失败 42" || got[0].Level != "error" || got[0].Caller != want {
		t.Fatalf("错误日志 = %+v，代码位置应为 %s", got[0], want)
	}
	if got[1].Event != "process_started" || got[1].Message != "启动 v1" || got[1].Caller != fmt.Sprintf("logsink/%s:%d", filepath.Base(file), line+3) {
		t.Fatalf("关键事件 = %+v", got[1])
	}
}

// 配置里已经写了 writer: moox 时不重复添加；配置为空时报错，和 tRPC 自带的装配器一致。
func TestFactoryConfigEdgeCases(t *testing.T) {
	previous := log.GetDefaultLogger()
	current.Store(nil)
	t.Cleanup(func() {
		log.SetLogger(previous)
		current.Store(nil)
	})
	if err := (defaultFactory{}).Setup("default", configDecoder{}); err == nil {
		t.Fatal("没有任何输出的配置应报错")
	}
	if err := (defaultFactory{}).Setup("default", nil); err == nil {
		t.Fatal("没有配置时应报错")
	}
	if err := (defaultFactory{}).Setup("default", configDecoder{outputs: log.Config{{Writer: "console", Level: "fatal"}, {Writer: WriterName, Level: "info"}}}); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
}
