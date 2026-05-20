package local

import (
	"context"
	"errors"
)

type BinaryInfo struct {
	Name string
	Path string
}

// Prefers tofu over terraform when both are present.
func DetectBinary() (BinaryInfo, error) {
	for _, name := range []string{"tofu", "terraform"} {
		if path, err := lookPath(name); err == nil {
			return BinaryInfo{Name: name, Path: path}, nil
		}
	}
	return BinaryInfo{}, ErrNoBinary
}

func DetectAll() []BinaryInfo {
	var out []BinaryInfo
	for _, name := range []string{"tofu", "terraform"} {
		if path, err := lookPath(name); err == nil {
			out = append(out, BinaryInfo{Name: name, Path: path})
		}
	}
	return out
}

var ErrNoBinary = errors.New("neither tofu nor terraform found on PATH")

type BinaryResolver interface {
	Resolve(ctx context.Context, engine, version string) (BinaryInfo, error)
}

type pathResolver struct{}

func (pathResolver) Resolve(_ context.Context, _, _ string) (BinaryInfo, error) {
	return DetectBinary()
}
