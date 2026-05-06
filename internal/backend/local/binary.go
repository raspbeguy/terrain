package local

import (
	"context"
	"errors"
)

// BinaryInfo describes a tofu/terraform CLI installation discovered on PATH.
// Inside a Flatpak sandbox, Path is the host's absolute path; the binary is
// invoked via flatpak-spawn --host and isn't directly stat-able from the
// sandbox.
type BinaryInfo struct {
	Name string // "tofu" or "terraform"
	Path string // absolute path to the binary (host PATH when running in Flatpak)
}

// DetectBinary searches PATH for tofu and terraform (in that order). Returns
// the first found; ErrNoBinary if neither is installed. Inside Flatpak the
// search runs on the host PATH via flatpak-spawn.
func DetectBinary() (BinaryInfo, error) {
	for _, name := range []string{"tofu", "terraform"} {
		if path, err := lookPath(name); err == nil {
			return BinaryInfo{Name: name, Path: path}, nil
		}
	}
	return BinaryInfo{}, ErrNoBinary
}

// DetectAll returns whatever subset of {tofu, terraform} is installed.
func DetectAll() []BinaryInfo {
	var out []BinaryInfo
	for _, name := range []string{"tofu", "terraform"} {
		if path, err := lookPath(name); err == nil {
			out = append(out, BinaryInfo{Name: name, Path: path})
		}
	}
	return out
}

// ErrNoBinary is returned when neither tofu nor terraform is on PATH.
var ErrNoBinary = errors.New("neither tofu nor terraform found on PATH")

// BinaryResolver impls: pathResolver (host PATH), managedResolver (cache).
type BinaryResolver interface {
	Resolve(ctx context.Context, engine, version string) (BinaryInfo, error)
}

// pathResolver ignores engine and version.
type pathResolver struct{}

func (pathResolver) Resolve(_ context.Context, _, _ string) (BinaryInfo, error) {
	return DetectBinary()
}
