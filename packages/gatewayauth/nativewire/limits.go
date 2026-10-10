// Package nativewire installs the process-wide tRPC frame ceiling before any
// client or listener starts. Service catalog limits remain the stricter
// per-method request/response boundary enforced by the host gateway.
package nativewire

import trpc "trpc.group/trpc-go/trpc-go"

// MaxFrameBytes accommodates the 48 MiB service envelope plus tRPC metadata.
// In particular, a 32 MiB egress body expands when JSON encodes its bytes.
const MaxFrameBytes = 64 << 20

func init() {
	trpc.DefaultMaxFrameSize = MaxFrameBytes
}
