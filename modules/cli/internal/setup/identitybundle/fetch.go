package identitybundle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"reflect"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

// Download is implemented by the verified setup SSH client's Download method.
// Neither callback errors nor remote output are included in material errors.
type Download func(context.Context, string, io.Writer) (int64, error)

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	overflow bool
}

func (b *boundedBuffer) Write(raw []byte) (int, error) {
	if int64(len(raw)) > b.limit-int64(b.buffer.Len()) {
		b.overflow = true
		return 0, errors.New("host material download exceeded declared size")
	}
	return b.buffer.Write(raw)
}

func downloadFile(ctx context.Context, download Download, source string, max int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	buffer := &boundedBuffer{limit: max}
	n, err := download(ctx, source, buffer)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil || buffer.overflow || n != int64(buffer.buffer.Len()) || n == 0 {
		return nil, errors.New("host material download failed or returned an invalid byte count")
	}
	return buffer.buffer.Bytes(), nil
}

// Fetch consumes public metadata returned by Admin over verified SSH, then
// independently pins topology, snapshot and CA. It verifies all private bytes
// before publishing a fresh local directory. A nonnil result on publication
// error identifies the directory that was renamed successfully.
func Fetch(ctx context.Context, download Download, source hostbundle.Metadata, options Options, destination string) (*Material, error) {
	if download == nil {
		return nil, errors.New("host material requires a verified SSH downloader")
	}
	if err := validateMetadata(source, options); err != nil {
		return nil, err
	}
	raw, err := downloadFile(ctx, download, path.Join(source.BundleDir, "bundle.json"), maxMetadata)
	if err != nil {
		return nil, err
	}
	var metadata hostbundle.Metadata
	if err := canonical(raw, &metadata); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(metadata, source) {
		return nil, errors.New("downloaded host material differs from the Admin response")
	}
	files := map[string][]byte{}
	for _, item := range source.Files {
		content, err := downloadFile(ctx, download, path.Join(source.BundleDir, item.Path), item.Size)
		if err != nil {
			return nil, err
		}
		if int64(len(content)) != item.Size || digest(content) != item.SHA256 {
			return nil, errors.New("downloaded host material digest or length differs from its inventory")
		}
		files[item.Path] = content
	}
	material, err := verify(metadata, files, options)
	if err != nil {
		return nil, err
	}
	payloads := make(map[string][]byte, len(files)+1)
	for name, content := range files {
		payloads[name] = content
	}
	payloads["bundle.json"] = raw
	published, err := fsutil.PublishPrivate(ctx, destination, payloads)
	if published {
		return material, err
	}
	return nil, err
}
