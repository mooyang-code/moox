//go:build !windows

package servicehost

import "context"

func install(Spec) error     { return ErrUnsupported }
func uninstall(string) error { return ErrUnsupported }
func start(string) error     { return ErrUnsupported }
func stop(string) error      { return ErrUnsupported }

func run(_ string, serve func(ctx context.Context) error) error {
	return serve(context.Background())
}
